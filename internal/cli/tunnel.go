package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"

	"github.com/spf13/cobra"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func newTunnelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tunnel [<ref>]",
		Short: "TCP tunnel",
		Args:  maxNArgs(1, "usage: rdcli tunnel <id> -L lport:rhost:rport [--once]"),
		RunE:  runTunnel,
	}
	cmd.Flags().Bool("once", false, "single stream mode")
	cmd.Flags().StringP("listen", "L", "", "local to remote port forwarding spec")
	return cmd
}

type forwardSpec struct {
	localPort int
	host      string
	port      int32
}

func parseForwardSpec(s string) (forwardSpec, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return forwardSpec{}, fmt.Errorf("invalid -L spec %q, want lport:rhost:rport", s)
	}
	lport, err := strconv.Atoi(parts[0])
	if err != nil || lport < 0 || lport > 65535 {
		return forwardSpec{}, fmt.Errorf("invalid local port %q", parts[0])
	}
	rport, err := strconv.Atoi(parts[2])
	if err != nil || rport < 1 || rport > 65535 {
		return forwardSpec{}, fmt.Errorf("invalid remote port %q", parts[2])
	}
	return forwardSpec{localPort: lport, host: parts[1], port: int32(rport)}, nil
}

func runTunnel(cmd *cobra.Command, args []string) error {
	peerID, password, remArgs, err := resolveTarget(cmd, args)
	if err != nil {
		return err
	}
	if len(remArgs) > 0 {
		return fmt.Errorf("unexpected arguments: %v", remArgs)
	}
	once, _ := cmd.Flags().GetBool("once")
	specStr, _ := cmd.Flags().GetString("listen")
	if specStr == "" {
		return usageErrorf("missing -L lport:rhost:rport")
	}
	spec, err := parseForwardSpec(specStr)
	if err != nil {
		return err
	}
	opts := dialOpts(cmd, peerID, password, pb.ConnType_PORT_FORWARD)

	if once {
		return runTunnelOnce(cmd, opts, spec)
	}
	return runTunnelListener(cmd, opts, spec)
}

func newTunnelConn(opts hbb.DialOpts, spec forwardSpec) (*hbb.Tunnel, error) {
	return hbb.NewTunnel(opts, spec.host, spec.port)
}

func runTunnelOnce(cmd *cobra.Command, opts hbb.DialOpts, spec forwardSpec) error {
	stdio, err := newTunnelStdio(os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	defer stdio.Close()
	t, err := newTunnelConn(opts, spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tunnel: %v\n", err)
		return err
	}
	defer t.Close()
	stop := context.AfterFunc(cmd.Context(), func() {
		t.Close()
		stdio.Close()
	})
	defer stop()
	err = t.Pipe(stdio)
	if cmd.Context().Err() != nil {
		return cmd.Context().Err()
	}
	return err
}

func runTunnelListener(cmd *cobra.Command, opts hbb.DialOpts, spec forwardSpec) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", spec.localPort))
	if err != nil {
		return fmt.Errorf("bind 127.0.0.1:%d: %w", spec.localPort, err)
	}
	defer ln.Close()
	stop := context.AfterFunc(cmd.Context(), func() { ln.Close() })
	defer stop()
	fmt.Fprintf(os.Stderr, "listening on 127.0.0.1:%d, forwarding to %s:%d\n", spec.localPort, spec.host, spec.port)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if cmd.Context().Err() != nil {
				return cmd.Context().Err()
			}
			return err
		}
		go func() {
			defer conn.Close()
			t, err := newTunnelConn(opts, spec)
			if err != nil {
				fmt.Fprintf(os.Stderr, "tunnel: %v\n", err)
				return
			}
			defer t.Close()
			stop := context.AfterFunc(cmd.Context(), func() {
				t.Close()
				conn.Close()
			})
			defer stop()
			_ = t.Pipe(conn)
		}()
	}
}
