package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"
)

func TestClipboardExplicitInput(t *testing.T) {
	for _, test := range []struct {
		name  string
		args  []string
		stdin bool
		input string
		want  string
		fail  bool
	}{
		{"text", []string{"hi\n世界"}, false, "ignored", "hi\n世界", false},
		{"empty", []string{""}, false, "", "", false},
		{"literal dash", []string{"-"}, false, "", "-", false},
		{"stdin", nil, true, "\n hello \n", "\n hello \n", false},
		{"missing", nil, false, "not implicit stdin", "", true},
		{"conflict", []string{"text"}, true, "", "", true},
		{"multiple", []string{"one", "two"}, false, "", "", true},
		{"oversized stdin", nil, true, strings.Repeat("a", hbb.MaxClipboardTextBytes+1), "", true},
		{"oversized text", []string{strings.Repeat("a", hbb.MaxClipboardTextBytes+1)}, false, "", "", true},
		{"invalid utf8", nil, true, string([]byte{0xff}), "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := clipboardInput(test.args, test.stdin, strings.NewReader(test.input))
			if (err != nil) != test.fail || (!test.fail && got != test.want) {
				t.Fatalf("input result length = %d, error = %v", len(got), err)
			}
		})
	}
}

func TestClipboardGetOutput(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		cmd := &cobra.Command{}
		cmd.Flags().Bool("json", asJSON, "")
		var output bytes.Buffer
		cmd.SetOut(&output)
		const text = "exact\n文本"
		if err := writeClipboardGet(cmd, text); err != nil {
			t.Fatal(err)
		}
		if asJSON {
			var data struct {
				Version int    `json:"version"`
				Text    string `json:"text"`
			}
			if err := json.Unmarshal(output.Bytes(), &data); err != nil || data.Version != 1 || data.Text != text {
				t.Fatalf("JSON output = %q, %v", output.String(), err)
			}
		} else if output.String() != text {
			t.Fatalf("text output changed bytes: %q", output.String())
		}
	}
}

type clipboardFailWriter struct{}

func (clipboardFailWriter) Write([]byte) (int, error) { return 0, errors.New("output failed") }

func TestClipboardOutputFailure(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetOut(clipboardFailWriter{})
	if err := writeClipboardGet(cmd, "text"); err == nil {
		t.Fatal("output failure swallowed")
	}
}

type clipboardEndlessInput struct{ read int }

func (r *clipboardEndlessInput) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	r.read += len(p)
	return len(p), nil
}

func TestClipboardInputReadBound(t *testing.T) {
	input := &clipboardEndlessInput{}
	if _, err := clipboardInput(nil, true, input); err == nil {
		t.Fatal("accepted oversized stdin")
	}
	if input.read != hbb.MaxClipboardTextBytes+1 {
		t.Fatalf("read %d bytes from oversized stdin", input.read)
	}
}
