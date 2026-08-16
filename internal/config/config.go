package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Peer struct {
	Name     string
	Password string
}

type Config struct {
	Server      string
	Key         string
	AccessToken string
	APIServer   string
	APIUsername string
	APIPassword string
	Peers       map[string]Peer
	Tags        map[string][]string
}

func Path() string {
	if p := os.Getenv("RDC_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "rdcli", "config.toml")
}

func Load() (*Config, error) {
	cfg := &Config{Peers: map[string]Peer{}, Tags: map[string][]string{}}
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", Path(), err)
	}
	if err := cfg.parse(data); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", Path(), err)
	}
	return cfg, nil
}

func (c *Config) parse(data []byte) error {
	section := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			header := strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			header = strings.TrimSpace(header)
			switch {
			case header == "tags":
				section = "tags"
			case strings.HasPrefix(header, "peers."):
				id, err := unquote(strings.TrimSpace(strings.TrimPrefix(header, "peers.")))
				if err != nil {
					return err
				}
				section = "peer:" + id
			default:
				section = ""
			}
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		value := strings.TrimSpace(line[eq+1:])
		switch {
		case section == "tags":
			id, err := unquote(key)
			if err != nil {
				return err
			}
			tags, err := parseStringArray(value)
			if err != nil {
				return err
			}
			sort.Strings(tags)
			c.Tags[id] = tags
		case strings.HasPrefix(section, "peer:"):
			id := strings.TrimPrefix(section, "peer:")
			p := c.Peers[id]
			if key == "password" {
				p.Password = parseString(value)
			} else if key == "name" {
				p.Name = parseString(value)
			}
			c.Peers[id] = p
		default:
			switch key {
			case "server":
				c.Server = parseString(value)
			case "key":
				c.Key = parseString(value)
			case "access_token":
				c.AccessToken = parseString(value)
			case "api_server":
				c.APIServer = parseString(value)
			case "api_username":
				c.APIUsername = parseString(value)
			case "api_password":
				c.APIPassword = parseString(value)
			}
		}
	}
	return nil
}

func (c *Config) Save() error {
	var b strings.Builder
	b.WriteString("server = ")
	b.WriteString(quote(c.Server))
	b.WriteByte('\n')
	b.WriteString("key = ")
	b.WriteString(quote(c.Key))
	b.WriteByte('\n')
	b.WriteString("access_token = ")
	b.WriteString(quote(c.AccessToken))
	b.WriteByte('\n')
	b.WriteString("api_server = ")
	b.WriteString(quote(c.APIServer))
	b.WriteByte('\n')
	b.WriteString("api_username = ")
	b.WriteString(quote(c.APIUsername))
	b.WriteByte('\n')
	b.WriteString("api_password = ")
	b.WriteString(quote(c.APIPassword))
	b.WriteByte('\n')

	peerIDs := sortedKeys(c.Peers)
	for _, id := range peerIDs {
		b.WriteByte('\n')
		b.WriteString("[peers.")
		b.WriteString(quote(id))
		b.WriteString("]\n")
		p := c.Peers[id]
		b.WriteString("password = ")
		b.WriteString(quote(p.Password))
		b.WriteByte('\n')
		if p.Name != "" {
			b.WriteString("name = ")
			b.WriteString(quote(p.Name))
			b.WriteByte('\n')
		}
	}

	tagIDs := sortedStringKeys(c.Tags)
	if len(tagIDs) > 0 {
		b.WriteByte('\n')
		b.WriteString("[tags]\n")
		for _, id := range tagIDs {
			b.WriteString(quote(id))
			b.WriteString(" = [")
			for i, t := range c.Tags[id] {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(quote(t))
			}
			b.WriteString("]\n")
		}
	}

	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

func (c *Config) PasswordForPeer(id string) string {
	if p, ok := c.Peers[id]; ok {
		return p.Password
	}
	return ""
}

func (c *Config) TagsFor(id string) []string {
	return c.Tags[id]
}

func (c *Config) AddTag(id, tag string) {
	for _, t := range c.Tags[id] {
		if t == tag {
			return
		}
	}
	c.Tags[id] = append(c.Tags[id], tag)
	sort.Strings(c.Tags[id])
}

func (c *Config) RemoveTag(id, tag string) {
	tags := c.Tags[id]
	out := tags[:0]
	for _, t := range tags {
		if t != tag {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		delete(c.Tags, id)
		return
	}
	c.Tags[id] = out
}

func (c *Config) SetPeersPassword(id, pw string) {
	p := c.Peers[id]
	p.Password = pw
	c.Peers[id] = p
}

func (c *Config) SetAccessToken(token string) {
	c.AccessToken = token
}

// GUIAccessToken returns the access token stored by the RustDesk desktop app
// (RustDesk_local.toml [options] access_token), or empty if not logged in.
func GUIAccessToken() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidates := []string{
		filepath.Join(home, "Library", "Preferences", "com.carriez.RustDesk", "RustDesk_local.toml"),
		filepath.Join(home, ".config", "rustdesk", "RustDesk_local.toml"),
		filepath.Join(home, ".local", "share", "rustdesk", "RustDesk_local.toml"),
		filepath.Join(home, "AppData", "Roaming", "RustDesk", "RustDesk_local.toml"),
	}
	for _, p := range candidates {
		if tok := parseLocalAccessToken(p); tok != "" {
			return tok
		}
	}
	return ""
}

// GUIAccessTokenPath returns the path of the desktop app config that holds a
// token, or "" if none found.
func GUIAccessTokenPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidates := []string{
		filepath.Join(home, "Library", "Preferences", "com.carriez.RustDesk", "RustDesk_local.toml"),
		filepath.Join(home, ".config", "rustdesk", "RustDesk_local.toml"),
		filepath.Join(home, ".local", "share", "rustdesk", "RustDesk_local.toml"),
		filepath.Join(home, "AppData", "Roaming", "RustDesk", "RustDesk_local.toml"),
	}
	for _, p := range candidates {
		if tok := parseLocalAccessToken(p); tok != "" {
			return p
		}
	}
	return ""
}

func parseLocalAccessToken(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	inOptions := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") {
			inOptions = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")) == "options"
			continue
		}
		if !inOptions {
			continue
		}
		if strings.HasPrefix(line, "access_token") {
			eq := strings.Index(line, "=")
			if eq >= 0 {
				v := strings.TrimSpace(line[eq+1:])
				return strings.Trim(v, "'\"")
			}
		}
	}
	return ""
}

func parseString(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, `"`) {
		if end := findClosingQuote(value); end >= 0 {
			s, err := unquote(value[:end+1])
			if err == nil {
				return s
			}
		}
	}
	if idx := strings.Index(value, "#"); idx >= 0 {
		value = strings.TrimSpace(value[:idx])
	}
	s, _ := unquote(value)
	return s
}

func parseStringArray(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "[") {
		return nil, fmt.Errorf("expected array, got %q", value)
	}
	end := strings.LastIndex(value, "]")
	if end < 0 {
		return nil, fmt.Errorf("unclosed array %q", value)
	}
	inner := value[1:end]
	if idx := strings.Index(inner, "#"); idx >= 0 {
		inner = inner[:idx]
	}
	var out []string
	for _, part := range strings.Split(inner, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		s, err := unquote(part)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func findClosingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '"' {
			return i
		}
	}
	return -1
}

func unquote(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var b strings.Builder
		inner := s[1 : len(s)-1]
		for i := 0; i < len(inner); i++ {
			ch := inner[i]
			if ch == '\\' && i+1 < len(inner) {
				i++
				switch inner[i] {
				case '"':
					b.WriteByte('"')
				case '\\':
					b.WriteByte('\\')
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				default:
					b.WriteByte(inner[i])
				}
				continue
			}
			b.WriteByte(ch)
		}
		return b.String(), nil
	}
	return s, nil
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func sortedKeys(m map[string]Peer) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sortedStringKeys(m map[string][]string) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
