package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/4nkitd/rustdesk-cli/internal/config"

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
	Source      string `json:"source,omitempty"`
	PasswordSet bool   `json:"password_set"`
}

func runAuthStatus(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageErrorf("usage: rdcli auth status")
	}
	cfg, err := config.Load()
	if err != nil {
		return errors.New("auth: could not load config")
	}

	source := ""
	switch {
	case cfg.AccessToken != "":
		source = "config"
	case config.GUIAccessToken() != "":
		source = "gui"
	}
	passwords := 0
	for _, peer := range cfg.Peers {
		if peer.Password != "" {
			passwords++
		}
	}

	st := authStatusJSON{
		Version:     2,
		LoggedIn:    source != "",
		Source:      source,
		PasswordSet: passwords > 0,
	}

	out := cmd.OutOrStdout()
	if isJSONFlag(cmd) {
		return json.NewEncoder(out).Encode(st)
	}

	if st.LoggedIn {
		fmt.Fprintf(out, "logged in (%s)\n", source)
	} else {
		fmt.Fprintln(out, "not logged in")
		source = "-"
	}
	fmt.Fprintf(out, "source:   %s\n", source)
	if st.PasswordSet {
		fmt.Fprintf(out, "saved passwords: %d peer(s)\n", passwords)
	}
	return nil
}

func runAuthImport(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageErrorf("usage: rdcli auth import-gui")
	}
	tok := config.GUIAccessToken()
	if tok == "" {
		return errors.New("auth: no token found in RustDesk desktop app config")
	}
	cfg, err := config.Load()
	if err != nil {
		return errors.New("auth: could not load config")
	}
	cfg.SetAccessToken(tok)
	if err := cfg.Save(); err != nil {
		return errors.New("auth: could not save config")
	}
	fmt.Fprintln(cmd.OutOrStdout(), "imported token from RustDesk desktop app config")
	return nil
}

func runAuthLogout(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageErrorf("usage: rdcli auth logout")
	}
	cfg, err := config.Load()
	if err != nil {
		return errors.New("auth: could not load config")
	}
	if cfg.AccessToken == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "no token stored")
		return nil
	}
	cfg.SetAccessToken("")
	if err := cfg.Save(); err != nil {
		return errors.New("auth: could not save config")
	}
	fmt.Fprintln(cmd.OutOrStdout(), "logged out")
	return nil
}

func runAuthSet(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return errors.New("auth: could not load config")
	}
	cfg.SetAccessToken(args[0])
	if err := cfg.Save(); err != nil {
		return errors.New("auth: could not save config")
	}
	fmt.Fprintln(cmd.OutOrStdout(), "token saved")
	return nil
}
