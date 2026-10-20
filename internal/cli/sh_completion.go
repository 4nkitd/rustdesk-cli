package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type CommandExitError struct {
	Code int
}

func (e *CommandExitError) Error() string {
	return fmt.Sprintf("remote command exited with status %d", e.Code)
}

func (e *CommandExitError) ExitCode() int { return e.Code }

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func shCommandScript(command, token string) string {
	// Encoded delimiters cannot be mistaken for completion when the PTY echoes input.
	script := "stty -echo; printf '\\036" + token + ":start\\037'; sh -c " + shQuote(command) +
		"; rdcli_status=$?; printf '\\036" + token + ":exit:%d\\037' \"$rdcli_status\""
	return "sh -c " + shQuote(script) + "\r\n"
}

func executeShCommand(terminal io.ReadWriter, output io.Writer, command string) error {
	if strings.ContainsRune(command, 0) {
		return fmt.Errorf("shell command cannot contain NUL bytes")
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	token := "rdcli-" + hex.EncodeToString(nonce[:])
	script := shCommandScript(command, token)
	if n, err := io.WriteString(terminal, script); err != nil {
		return fmt.Errorf("send remote command: %w", err)
	} else if n != len(script) {
		return io.ErrShortWrite
	}
	return readShCompletion(terminal, output, token)
}

func readShCompletion(input io.Reader, output io.Writer, token string) error {
	start := []byte("\x1e" + token + ":start\x1f")
	end := []byte("\x1e" + token + ":exit:")
	var pending []byte
	started := false
	buf := make([]byte, 4096)
	write := func(data []byte) error {
		n, err := output.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		return err
	}
	for {
		n, readErr := input.Read(buf)
		pending = append(pending, buf[:n]...)
		if !started {
			if i := bytes.Index(pending, start); i >= 0 {
				pending = pending[i+len(start):]
				started = true
			} else if len(pending) >= len(start) {
				pending = pending[len(pending)-len(start)+1:]
			}
		}
		if started {
			if i := bytes.Index(pending, end); i >= 0 {
				if err := write(pending[:i]); err != nil {
					return err
				}
				pending = pending[i:]
				status := pending[len(end):]
				if j := bytes.IndexByte(status, '\x1f'); j >= 0 {
					code, err := strconv.Atoi(string(status[:j]))
					if err != nil || j == 0 || j > 3 || code < 0 || code > 255 || bytes.IndexFunc(status[:j], func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
						return fmt.Errorf("invalid remote command completion status %q", status[:j])
					}
					if code != 0 {
						return &CommandExitError{Code: code}
					}
					return nil
				}
				if len(status) > 3 {
					return fmt.Errorf("invalid remote command completion status")
				}
			} else if count := len(pending) - len(end) + 1; count > 0 {
				if err := write(pending[:count]); err != nil {
					return err
				}
				pending = pending[count:]
			}
		}
		if readErr != nil {
			if started {
				if err := write(pending); err != nil {
					return err
				}
			}
			return fmt.Errorf("remote command ended before completion was confirmed: %w", readErr)
		}
	}
}
