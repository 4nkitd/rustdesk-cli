package cli

import (
	"strings"

	"rdcli/internal/config"
)

// ResolveRef takes a string ref (id, name, or tag) and returns the peer ID
// and password from config. Returns (ref, "", nil) if no config match.
func ResolveRef(cfg *config.Config, ref string) (peerID, password string, err error) {
	// 1. Exact ID match in peers
	if _, ok := cfg.Peers[ref]; ok {
		return ref, cfg.Peers[ref].Password, nil
	}
	// 2. Name match (case-insensitive substring)
	for id, p := range cfg.Peers {
		if strings.Contains(strings.ToLower(p.Name), strings.ToLower(ref)) {
			return id, p.Password, nil
		}
	}
	// 3. Tag match
	for id, tags := range cfg.Tags {
		for _, t := range tags {
			if strings.EqualFold(t, ref) {
				if p, ok := cfg.Peers[id]; ok {
					return id, p.Password, nil
				}
				return id, "", nil
			}
		}
	}
	// 4. No match — treat ref as raw ID
	if p, ok := cfg.Peers[ref]; ok {
		return ref, p.Password, nil
	}
	return ref, "", nil
}
