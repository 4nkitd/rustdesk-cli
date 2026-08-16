//go:build windows

package cli

import "os"

func notifySigwinch(ch chan<- os.Signal) {}
