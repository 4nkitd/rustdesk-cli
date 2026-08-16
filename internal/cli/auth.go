package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"rdcli/internal/config"

	"github.com/spf13/cobra"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "manage login token",
	}

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "show login status",
		RunE:  runAuthStatus,
	}
	importCmd := &cobra.Command{
		Use:   "import-gui",
		Short: "import login token from RustDesk desktop app",
		RunE:  runAuthImport,
	}
	logoutCmd := &cobra.Command{
		Use:   "logout",
		Short: "clear stored login token",
		RunE:  runAuthLogout,
	}
	setCmd := &cobra.Command{
		Use:   "set <token>",
		Short: "set login token explicitly",
		Args:  cobra.ExactArgs(1),
		RunE:  runAuthSet,
	}
	cmd.AddCommand(statusCmd, importCmd, logoutCmd, setCmd)
	return cmd
}

type authStatusJSON struct {
	Version     int    `json:"version"`
	LoggedIn    bool   `json:"logged_in"`
	Token       string `json:"token,omitempty"`
	Source      string `json:"source,omitempty"`
	GUIConfig   string `json:"gui_config,omitempty"`
	RDCConfig   string `json:"rdc_config,omitempty"`
	Server      string `json:"server,omitempty"`
	UserInfo    string `json:"user_info,omitempty"`
	PasswordSet bool   `json:"password_set"`
}

func runAuthStatus(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageErrorf("usage: rdcli auth status")
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth: %v\n", err)
		os.Exit(1)
		return nil
	}
	guiTok := config.GUIAccessToken()
	guiPath := config.GUIAccessTokenPath()

	source := ""
	tok := cfg.AccessToken
	switch {
	case tok != "":
		source = "config"
	case guiTok != "":
		tok = guiTok
		source = "gui"
	default:
		source = ""
	}

	st := authStatusJSON{
		Version:     1,
		LoggedIn:    tok != "",
		Token:       tok,
		Source:      source,
		GUIConfig:   guiPath,
		RDCConfig:   config.Path(),
		Server:      resolveServer(cmd, cfg),
		UserInfo:    guiUserInfo(guiPath),
		PasswordSet: len(cfg.Peers) > 0,
	}

	if isJSONFlag(cmd) {
		json.NewEncoder(os.Stdout).Encode(st)
		return nil
	}

	if st.LoggedIn {
		fmt.Printf("logged in (%s)\n", source)
		fmt.Printf("token:    %s\n", maskToken(tok))
	} else {
		fmt.Println("not logged in")
	}
	fmt.Printf("source:   %s\n", orDash(source))
	fmt.Printf("gui cfg:  %s\n", orDash(guiPath))
	fmt.Printf("rdc cfg:  %s\n", config.Path())
	if st.UserInfo != "" {
		fmt.Printf("user:     %s\n", st.UserInfo)
	}
	if st.PasswordSet {
		fmt.Printf("saved passwords: %d peer(s)\n", len(cfg.Peers))
	}
	return nil
}

func runAuthImport(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageErrorf("usage: rdcli auth import-gui")
	}
	tok := config.GUIAccessToken()
	if tok == "" {
		fmt.Fprintln(os.Stderr, "auth: no token found in RustDesk desktop app config")
		os.Exit(1)
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth: %v\n", err)
		os.Exit(1)
		return nil
	}
	cfg.SetAccessToken(tok)
	if err := cfg.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "auth: %v\n", err)
		os.Exit(1)
		return nil
	}
	fmt.Printf("imported token from %s\n", config.GUIAccessTokenPath())
	return nil
}

func runAuthLogout(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageErrorf("usage: rdcli auth logout")
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth: %v\n", err)
		os.Exit(1)
		return nil
	}
	if cfg.AccessToken == "" {
		fmt.Println("no token stored")
		return nil
	}
	cfg.SetAccessToken("")
	if err := cfg.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "auth: %v\n", err)
		os.Exit(1)
		return nil
	}
	fmt.Println("logged out")
	return nil
}

func runAuthSet(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.SetAccessToken(args[0])
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Println("token saved")
	return nil
}

func maskToken(tok string) string {
	if len(tok) <= 8 {
		return "****"
	}
	return tok[:4] + "..." + tok[len(tok)-4:]
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func guiUserInfo(guiPath string) string {
	if guiPath == "" {
		return ""
	}
	data, err := os.ReadFile(guiPath)
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
		if strings.HasPrefix(line, "user_info") {
			eq := strings.Index(line, "=")
			if eq >= 0 {
				v := strings.TrimSpace(line[eq+1:])
				return strings.Trim(v, "'\"")
			}
		}
	}
	return ""
}
