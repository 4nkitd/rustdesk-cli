package hbb

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/nacl/secretbox"
	"google.golang.org/protobuf/proto"

	pb "rdcli/internal/hbb/proto"
)

type KeyPair struct {
	Pub  []byte
	Priv []byte
}

func GenerateKeyPair() (KeyPair, error) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, err
	}
	return KeyPair{
		Pub:  pub[:],
		Priv: priv[:],
	}, nil
}

func DeriveSharedSecret(ourPriv, theirPub []byte) ([32]byte, error) {
	if len(ourPriv) != 32 || len(theirPub) != 32 {
		return [32]byte{}, errors.New("invalid key length")
	}
	var privKey, pubKey, sharedKey [32]byte
	copy(privKey[:], ourPriv)
	copy(pubKey[:], theirPub)
	box.Precompute(&sharedKey, &pubKey, &privKey)
	return sharedKey, nil
}

type Encryptor struct {
	key       [32]byte
	sendCount uint64
	recvCount uint64
}

func NewEncryptor(key [32]byte) *Encryptor {
	return &Encryptor{key: key}
}

func MakeNonce(counter uint64) [24]byte {
	var nonce [24]byte
	binary.LittleEndian.PutUint64(nonce[:8], counter)
	return nonce
}

// Encrypt increments the send counter before use, matching hbb_common tcp.rs.
func (e *Encryptor) Encrypt(plaintext []byte) []byte {
	e.sendCount++
	nonce := MakeNonce(e.sendCount)
	return secretbox.Seal(nil, plaintext, &nonce, &e.key)
}

// Decrypt increments the recv counter before use, matching hbb_common tcp.rs.
func (e *Encryptor) Decrypt(ciphertext []byte) ([]byte, error) {
	e.recvCount++
	nonce := MakeNonce(e.recvCount)
	out, ok := secretbox.Open(nil, ciphertext, &nonce, &e.key)
	if !ok {
		return nil, errors.New("decryption failed")
	}
	return out, nil
}

// VerifySignedID checks an ed25519-signed blob against the given ed25519
// public key and returns the embedded id and the X25519 public key. The blob
// layout matches sodiumoxide sign::sign: signature(64) || IdPk_proto. Mirrors
// decode_id_pk in rustdesk common.rs.
func VerifySignedID(signed, edPub []byte) (string, []byte, error) {
	if len(edPub) != ed25519.PublicKeySize {
		return "", nil, fmt.Errorf("hbb: invalid ed25519 public key length %d", len(edPub))
	}
	if len(signed) <= ed25519.SignatureSize {
		return "", nil, fmt.Errorf("hbb: signed id too short: %d", len(signed))
	}
	sig := signed[:ed25519.SignatureSize]
	msg := signed[ed25519.SignatureSize:]
	if !ed25519.Verify(edPub, msg, sig) {
		return "", nil, errors.New("hbb: signature mismatch")
	}
	idpk := &pb.IdPk{}
	if err := proto.Unmarshal(msg, idpk); err != nil {
		return "", nil, fmt.Errorf("hbb: unmarshal id_pk: %w", err)
	}
	pk := idpk.GetPk()
	if len(pk) != 32 {
		return "", nil, fmt.Errorf("hbb: invalid x25519 public key length %d", len(pk))
	}
	return idpk.GetId(), pk, nil
}

// VerifySignedBlob verifies a signature-first blob (64-byte signature ||
// message) against an ed25519 public key and returns the message. This is the
// layout used by hbbs KeyExchange: sig(64) || x25519_pubkey(32), matching
// sodiumoxide crypto::sign::verify used in rustdesk secure_tcp.
func VerifySignedBlob(blob, edPub []byte) ([]byte, error) {
	if len(edPub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("hbb: invalid ed25519 public key length %d", len(edPub))
	}
	if len(blob) <= ed25519.SignatureSize {
		return nil, fmt.Errorf("hbb: signed blob too short: %d", len(blob))
	}
	sig := blob[:ed25519.SignatureSize]
	msg := blob[ed25519.SignatureSize:]
	if !ed25519.Verify(edPub, msg, sig) {
		return nil, errors.New("hbb: signature mismatch in key exchange")
	}
	return msg, nil
}

// CreateSymmetricKeyMsg generates an ephemeral X25519 keypair and a fresh
// secretbox key, box-seals the key to theirPk with a zero nonce, and returns
// the ephemeral public key (asymmetric_value), the sealed key (symmetric_value)
// and the secretbox key. Matches create_symmetric_key_msg in common.rs.
func CreateSymmetricKeyMsg(theirPk []byte) ([]byte, []byte, [32]byte, error) {
	if len(theirPk) != 32 {
		return nil, nil, [32]byte{}, fmt.Errorf("hbb: invalid remote public key length %d", len(theirPk))
	}
	ourPub, ourPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, [32]byte{}, err
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, nil, [32]byte{}, err
	}
	var nonce [24]byte
	var peer, priv [32]byte
	copy(peer[:], theirPk)
	copy(priv[:], ourPriv[:])
	sealed := box.Seal(nil, key[:], &nonce, &peer, &priv)
	return ourPub[:], sealed, key, nil
}

// DecryptSymmetricKey recovers the secretbox key that the peer box-sealed to
// our ephemeral private key. Matches tcp::Encrypt::decode.
func DecryptSymmetricKey(symmetric, asymmetric, ourPriv []byte) ([32]byte, error) {
	if len(asymmetric) != 32 {
		return [32]byte{}, fmt.Errorf("hbb: invalid peer public key length %d", len(asymmetric))
	}
	var nonce [24]byte
	var peer, priv [32]byte
	copy(peer[:], asymmetric)
	copy(priv[:], ourPriv)
	key, ok := box.Open(nil, symmetric, &nonce, &peer, &priv)
	if !ok {
		return [32]byte{}, errors.New("hbb: box decryption failure")
	}
	if len(key) != 32 {
		return [32]byte{}, fmt.Errorf("hbb: invalid symmetric key length %d", len(key))
	}
	var out [32]byte
	copy(out[:], key)
	return out, nil
}
