package cli

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/4nkitd/rustdesk-cli/internal/config"
)

var customPeerID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{5,15}$`)

func ResolveRef(cfg *config.Config, ref string) (peerID, password string, err error) {
	if strings.TrimSpace(ref) == "" {
		return "", "", fmt.Errorf("machine reference must not be empty")
	}
	if p, ok := cfg.Peers[ref]; ok {
		return ref, p.Password, nil
	}
	if _, ok := cfg.Tags[ref]; ok {
		return ref, "", nil
	}
	candidates := make(map[string]struct{})
	lowerRef := strings.ToLower(ref)
	for id, p := range cfg.Peers {
		if strings.Contains(strings.ToLower(p.Name), lowerRef) {
			candidates[id] = struct{}{}
		}
	}
	for id, tags := range cfg.Tags {
		for _, t := range tags {
			if strings.EqualFold(t, ref) {
				candidates[id] = struct{}{}
				break
			}
		}
	}
	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > 1 {
		return "", "", fmt.Errorf("ambiguous machine reference %q: matches %s; use an exact ID", ref, strings.Join(ids, ", "))
	}
	if len(ids) == 1 {
		id := ids[0]
		if strings.TrimSpace(id) == "" {
			return "", "", fmt.Errorf("machine reference %q matches an empty configured ID", ref)
		}
		return id, cfg.Peers[id].Password, nil
	}
	if isRawPeerRef(ref) {
		return ref, "", nil
	}
	return "", "", fmt.Errorf("unknown machine reference %q: use a configured name or tag, a RustDesk ID, or an IP address", ref)
}

func isRawPeerRef(ref string) bool {
	if net.ParseIP(ref) != nil {
		return true
	}
	if host, port, err := net.SplitHostPort(ref); err == nil {
		n, err := strconv.ParseUint(port, 10, 16)
		return net.ParseIP(host) != nil && err == nil && n > 0
	}
	if ref == "" {
		return false
	}
	if strings.IndexFunc(ref, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
		return true
	}
	return customPeerID.MatchString(ref)
}
