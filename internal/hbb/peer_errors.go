package hbb

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"unicode"
)

type peerErrorState struct {
	mu       sync.Mutex
	captured bool
	secrets  [4]string
}

func peerTextWithoutControls(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
}

func (c *Conn) capturePeerSecrets() [4]string {
	c.peerErrors.mu.Lock()
	defer c.peerErrors.mu.Unlock()
	if !c.peerErrors.captured {
		c.peerErrors.secrets = [4]string{c.opts.Password, c.opts.Token, c.opts.Key, c.opts.OTP}
		for i, value := range c.peerErrors.secrets {
			c.peerErrors.secrets[i] = peerTextWithoutControls(value)
		}
		c.peerErrors.captured = true
	}
	return c.peerErrors.secrets
}

func (c *Conn) PeerText(text string) string {
	secrets := c.capturePeerSecrets()
	slices.SortFunc(secrets[:], func(a, b string) int { return len(b) - len(a) })
	text = peerTextWithoutControls(text)
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	// Markers or joined fragments must not reproduce a submitted credential.
	for {
		before := text
		for _, secret := range secrets {
			if secret != "" {
				text = strings.ReplaceAll(text, secret, "")
			}
		}
		if text == before {
			return text
		}
	}
}

func (c *Conn) PeerError(text string) error {
	return errors.New(c.PeerText(text))
}
