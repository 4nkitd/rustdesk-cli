package hbb

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

// Budgets apply to the whole wire message, including duplicate singular fields
// that protobuf would merge or overwrite. A manifest may contain 8,192 entries.
// Repeated-field limits aggregate by schema field across all nested occurrences.
const (
	maxPeerMessageBytes     = 64 << 20
	maxPeerMessageObjects   = 16384
	maxPeerRepeatedField    = 8192
	maxPeerRepeatedElements = 32768
	maxPeerWireFields       = 131072
	maxPeerMessageDepth     = 32
	maxPeerUnknownBytes     = 1 << 20
)

func decodePeerMessage(raw []byte, msg *pb.Message) error {
	return decodeBudgetedMessage(raw, msg)
}

func decodeRendezvousMessage(raw []byte, msg *pb.RendezvousMessage) error {
	return decodeBudgetedMessage(raw, msg)
}

func decodeBudgetedMessage(raw []byte, msg proto.Message) error {
	if msg == nil || !msg.ProtoReflect().IsValid() {
		return errors.New("hbb: nil message destination")
	}
	if len(raw) > maxPeerMessageBytes {
		return errors.New("hbb: message exceeds wire byte limit")
	}
	budget := peerMessageBudget{repeated: make(map[protoreflect.FullName]int)}
	if _, err := budget.scan(raw, msg.ProtoReflect().Descriptor(), 1, 0); err != nil {
		return fmt.Errorf("hbb: message: %w", err)
	}
	return (proto.UnmarshalOptions{RecursionLimit: maxPeerMessageDepth}).Unmarshal(raw, msg)
}

type peerMessageBudget struct {
	objects      int
	fields       int
	elements     int
	unknownBytes int
	repeated     map[protoreflect.FullName]int
}

func (b *peerMessageBudget) addRepeated(field protoreflect.FieldDescriptor, count int) error {
	name := field.FullName()
	if count > maxPeerRepeatedField-b.repeated[name] {
		return fmt.Errorf("repeated field %s exceeds %d elements", name, maxPeerRepeatedField)
	}
	if count > maxPeerRepeatedElements-b.elements {
		return errors.New("repeated element budget exceeded")
	}
	b.repeated[name] += count
	b.elements += count
	return nil
}

func (b *peerMessageBudget) scan(raw []byte, descriptor protoreflect.MessageDescriptor, depth int, group protowire.Number) (int, error) {
	if depth > maxPeerMessageDepth {
		return 0, errors.New("message depth limit exceeded")
	}
	b.objects++
	if b.objects > maxPeerMessageObjects {
		return 0, errors.New("message object budget exceeded")
	}
	remaining := raw
	for len(remaining) > 0 {
		number, wireType, tagLen := protowire.ConsumeTag(remaining)
		if tagLen < 0 {
			return 0, protowire.ParseError(tagLen)
		}
		if !number.IsValid() {
			return 0, errors.New("invalid field number")
		}
		if wireType == protowire.EndGroupType {
			if group == 0 || group != number {
				return 0, errors.New("unexpected end group")
			}
			return len(raw) - len(remaining) + tagLen, nil
		}
		b.fields++
		if b.fields > maxPeerWireFields {
			return 0, errors.New("wire field budget exceeded")
		}
		var field protoreflect.FieldDescriptor
		if descriptor != nil {
			field = descriptor.Fields().ByNumber(protoreflect.FieldNumber(number))
		}
		known := field != nil && peerFieldWireType(field.Kind()) == wireType
		packed := field != nil && field.IsList() && wireType == protowire.BytesType && peerPackable(field.Kind())
		value := remaining[tagLen:]
		var payload []byte
		var valueLen int
		switch wireType {
		case protowire.BytesType:
			payload, valueLen = protowire.ConsumeBytes(value)
		case protowire.StartGroupType:
			var child protoreflect.MessageDescriptor
			if known {
				child = field.Message()
			}
			var err error
			valueLen, err = b.scan(value, child, depth+1, number)
			if err != nil {
				return 0, err
			}
		default:
			valueLen = protowire.ConsumeFieldValue(number, wireType, value)
		}
		if valueLen < 0 {
			return 0, protowire.ParseError(valueLen)
		}
		if packed {
			if err := b.scanPacked(payload, field); err != nil {
				return 0, err
			}
		} else if known {
			if field.IsList() {
				if err := b.addRepeated(field, 1); err != nil {
					return 0, err
				}
			}
			if field.Kind() == protoreflect.MessageKind {
				if _, err := b.scan(payload, field.Message(), depth+1, 0); err != nil {
					return 0, err
				}
			}
		} else if descriptor != nil {
			// Unknown length-delimited values are opaque. Unknown groups are
			// scanned for structural budgets, then charged once including children.
			if tagLen+valueLen > maxPeerUnknownBytes-b.unknownBytes {
				return 0, errors.New("unknown field byte budget exceeded")
			}
			b.unknownBytes += tagLen + valueLen
		}
		remaining = value[valueLen:]
	}
	if group != 0 {
		return 0, errors.New("unterminated group")
	}
	return len(raw), nil
}

func (b *peerMessageBudget) scanPacked(raw []byte, field protoreflect.FieldDescriptor) error {
	wireType := peerFieldWireType(field.Kind())
	if wireType == protowire.Fixed32Type || wireType == protowire.Fixed64Type {
		width := 4
		if wireType == protowire.Fixed64Type {
			width = 8
		}
		if len(raw)%width != 0 {
			return errors.New("truncated packed fixed-width field")
		}
		return b.addRepeated(field, len(raw)/width)
	}
	for len(raw) > 0 {
		_, n := protowire.ConsumeVarint(raw)
		if n < 0 {
			return protowire.ParseError(n)
		}
		if err := b.addRepeated(field, 1); err != nil {
			return err
		}
		raw = raw[n:]
	}
	return nil
}

func peerPackable(kind protoreflect.Kind) bool {
	return kind != protoreflect.MessageKind && kind != protoreflect.GroupKind && kind != protoreflect.StringKind && kind != protoreflect.BytesKind
}

func peerFieldWireType(kind protoreflect.Kind) protowire.Type {
	switch kind {
	case protoreflect.DoubleKind, protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind:
		return protowire.Fixed64Type
	case protoreflect.FloatKind, protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind:
		return protowire.Fixed32Type
	case protoreflect.StringKind, protoreflect.BytesKind, protoreflect.MessageKind:
		return protowire.BytesType
	case protoreflect.GroupKind:
		return protowire.StartGroupType
	default:
		return protowire.VarintType
	}
}
