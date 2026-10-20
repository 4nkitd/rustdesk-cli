package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/4nkitd/rustdesk-cli/internal/config"

	"github.com/spf13/cobra"
)

func newImportGuiCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import-gui",
		Short: "import peers from local RustDesk installation",
		RunE:  runImport,
	}
	cmd.Flags().String("source", "", "path to RustDesk peers directory")
	cmd.Flags().Bool("force", false, "overwrite existing peer configuration")
	return cmd
}

func runImport(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageErrorf("usage: rdcli import-gui [--source <path>] [--force]")
	}

	force, _ := cmd.Flags().GetBool("force")
	srcDir, err := resolveSourceDir(cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import-gui: %v\n", err)
		os.Exit(1)
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "import-gui: %v\n", err)
		os.Exit(1)
		return nil
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import-gui: %v\n", err)
		os.Exit(1)
		return nil
	}

	importedCount := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}

		filePath := filepath.Join(srcDir, entry.Name())
		id, password, name, info, err := parsePeerFile(filePath)
		if err != nil || id == "" {
			continue
		}

		existingPeer, exists := cfg.Peers[id]
		if exists && existingPeer.Password != "" && !force {
			continue
		}

		p := existingPeer
		p.Password = password
		if name != "" {
			p.Name = name
		}
		cfg.Peers[id] = p

		if platform := info["platform"]; platform != "" {
			cfg.AddTag(id, strings.ToLower(platform))
		} else if hostname := info["hostname"]; hostname != "" {
			cfg.AddTag(id, strings.ToLower(hostname))
		}

		importedCount++
	}

	if err := cfg.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "import-gui: %v\n", err)
		os.Exit(1)
		return nil
	}

	fmt.Printf("imported %d peers\n", importedCount)
	return nil
}

func resolveSourceDir(cmd *cobra.Command) (string, error) {
	src, _ := cmd.Flags().GetString("source")
	if src != "" {
		if strings.HasPrefix(src, "~/") || src == "~" {
			home, err := os.UserHomeDir()
			if err == nil {
				if src == "~" {
					src = home
				} else {
					src = filepath.Join(home, src[2:])
				}
			}
		}
		info, err := os.Stat(src)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("source directory %q does not exist or is not a directory", src)
		}
		return src, nil
	}
	return detectRustDeskPeersDir()
}

func detectRustDeskPeersDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}

	macPath := filepath.Join(home, "Library", "Preferences", "com.carriez.RustDesk", "peers")
	if info, err := os.Stat(macPath); err == nil && info.IsDir() {
		return macPath, nil
	}

	linuxPath1 := filepath.Join(home, ".local", "share", "rustdesk", "peers")
	if info, err := os.Stat(linuxPath1); err == nil && info.IsDir() {
		return linuxPath1, nil
	}

	linuxPath2 := filepath.Join(home, ".config", "rustdesk", "peers")
	if info, err := os.Stat(linuxPath2); err == nil && info.IsDir() {
		return linuxPath2, nil
	}

	return "", fmt.Errorf("no RustDesk peers directory found")
}

func parsePeerFile(path string) (id, password, name string, info map[string]string, err error) {
	filename := filepath.Base(path)
	if !strings.HasSuffix(filename, ".toml") {
		return "", "", "", nil, fmt.Errorf("not a toml file")
	}
	id = strings.TrimSuffix(filename, ".toml")

	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", "", nil, err
	}

	info = make(map[string]string)
	lines := strings.Split(string(data), "\n")

	var inPasswordArray bool
	var passwordBytes []byte
	var currentSection string

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if inPasswordArray {
			idx := strings.Index(line, "]")
			sub := line
			if idx >= 0 {
				sub = line[:idx]
				inPasswordArray = false
			}
			parseBytes(sub, &passwordBytes)
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			currentSection = strings.TrimSpace(currentSection)
			continue
		}

		if currentSection == "" {
			if strings.HasPrefix(line, "password") {
				eq := strings.Index(line, "=")
				if eq >= 0 {
					rest := strings.TrimSpace(line[eq+1:])
					if bracket := strings.Index(rest, "["); bracket >= 0 {
						afterBracket := rest[bracket+1:]
						idx := strings.Index(afterBracket, "]")
						if idx >= 0 {
							parseBytes(afterBracket[:idx], &passwordBytes)
						} else {
							parseBytes(afterBracket, &passwordBytes)
							inPasswordArray = true
						}
					}
				}
			}
		} else if currentSection == "info" {
			eq := strings.Index(line, "=")
			if eq >= 0 {
				k := strings.TrimSpace(line[:eq])
				v := strings.TrimSpace(line[eq+1:])
				info[k] = parseTOMLString(v)
			}
		}
	}

	password = string(passwordBytes)

	username := info["username"]
	hostname := info["hostname"]
	if username != "" && hostname != "" {
		name = username + "-" + hostname
	} else if username != "" {
		name = username
	} else if hostname != "" {
		name = hostname
	}

	return id, password, name, info, nil
}

func parseBytes(s string, out *[]byte) {
	if idx := strings.Index(s, "#"); idx >= 0 {
		s = s[:idx]
	}
	parts := strings.Split(s, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if quoted, err := strconv.Unquote(p); err == nil && len(quoted) == 1 {
			*out = append(*out, quoted[0])
			continue
		}
		val, err := strconv.Atoi(p)
		if err == nil && val >= 0 && val <= 255 {
			*out = append(*out, byte(val))
		}
	}
}

func parseTOMLString(v string) string {
	v = strings.TrimSpace(v)
	if idx := strings.Index(v, "#"); idx >= 0 {
		v = strings.TrimSpace(v[:idx])
	}
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
