package hbb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"rdcli/internal/config"
)

type Device struct {
	ID       string
	Name     string
	Platform string
	Tags     []string
	Online   bool
	LastSeen string
	Source   string
}

type DeviceSource interface {
	List() ([]Device, error)
	Tags(id string) []string
	AddTag(id, tag string) error
	RemoveTag(id, tag string) error
}

const apiTimeout = 30 * time.Second

var apiClient = &http.Client{Timeout: apiTimeout}

type localSource struct {
	cfg *config.Config
}

func LocalSource(cfg *config.Config) DeviceSource {
	return &localSource{cfg: cfg}
}

func (s *localSource) List() ([]Device, error) {
	idMap := make(map[string]bool)
	for id := range s.cfg.Tags {
		idMap[id] = true
	}
	for id := range s.cfg.Peers {
		idMap[id] = true
	}
	ids := make([]string, 0, len(idMap))
	for id := range idMap {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	devs := make([]Device, 0, len(ids))
	for _, id := range ids {
		tags := append([]string(nil), s.cfg.Tags[id]...)
		sort.Strings(tags)
		name := ""
		if p, ok := s.cfg.Peers[id]; ok {
			name = p.Name
		}
		devs = append(devs, Device{ID: id, Name: name, Tags: tags, Source: "local"})
	}
	return devs, nil
}

func (s *localSource) Tags(id string) []string {
	return append([]string(nil), s.cfg.TagsFor(id)...)
}

func (s *localSource) AddTag(id, tag string) error {
	s.cfg.AddTag(id, tag)
	return s.cfg.Save()
}

func (s *localSource) RemoveTag(id, tag string) error {
	s.cfg.RemoveTag(id, tag)
	return s.cfg.Save()
}

type apiEntry struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Platform string   `json:"platform"`
	Tags     []string `json:"tags"`
}

type apiSource struct {
	base    string
	token   string
	entries map[string]*apiEntry
}

func NewDeviceSource(cfg *config.Config) (DeviceSource, error) {
	if cfg != nil && cfg.APIServer != "" {
		return APISource(cfg)
	}
	return LocalSource(cfg), nil
}

func APISource(cfg *config.Config) (DeviceSource, error) {
	base := strings.TrimRight(cfg.APIServer, "/")
	token, err := apiLogin(base, cfg.APIUsername, cfg.APIPassword)
	if err != nil {
		return nil, err
	}
	entries, err := apiGetAB(base, token)
	if err != nil {
		return nil, err
	}
	src := &apiSource{base: base, token: token, entries: map[string]*apiEntry{}}
	for i := range entries {
		e := entries[i]
		if e.ID != "" {
			src.entries[e.ID] = &e
		}
	}
	return src, nil
}

func (s *apiSource) List() ([]Device, error) {
	ids := make([]string, 0, len(s.entries))
	for id := range s.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	devs := make([]Device, 0, len(ids))
	for _, id := range ids {
		e := s.entries[id]
		tags := append([]string(nil), e.Tags...)
		sort.Strings(tags)
		devs = append(devs, Device{ID: id, Name: e.Name, Platform: e.Platform, Tags: tags, Source: "api"})
	}
	return devs, nil
}

func (s *apiSource) Tags(id string) []string {
	if e, ok := s.entries[id]; ok {
		return append([]string(nil), e.Tags...)
	}
	return nil
}

func (s *apiSource) AddTag(id, tag string) error {
	e, ok := s.entries[id]
	if !ok {
		return fmt.Errorf("unknown device %s", id)
	}
	if hasTag(e.Tags, tag) {
		return nil
	}
	next := append(append([]string(nil), e.Tags...), tag)
	sort.Strings(next)
	if err := s.pushAB(id, next); err != nil {
		return err
	}
	e.Tags = next
	return nil
}

func (s *apiSource) RemoveTag(id, tag string) error {
	e, ok := s.entries[id]
	if !ok {
		return fmt.Errorf("unknown device %s", id)
	}
	next := make([]string, 0, len(e.Tags))
	for _, t := range e.Tags {
		if t != tag {
			next = append(next, t)
		}
	}
	if len(next) == len(e.Tags) {
		return nil
	}
	sort.Strings(next)
	if err := s.pushAB(id, next); err != nil {
		return err
	}
	e.Tags = next
	return nil
}

func (s *apiSource) pushAB(id string, tags []string) error {
	entries := make([]apiEntry, 0, len(s.entries))
	ids := make([]string, 0, len(s.entries))
	for k := range s.entries {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	for _, k := range ids {
		e := *s.entries[k]
		if k == id {
			e.Tags = tags
		}
		entries = append(entries, e)
	}
	body, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode address book: %w", err)
	}
	req, err := http.NewRequest(http.MethodPut, s.base+"/api/ab", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build address book update: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := apiClient.Do(req)
	if err != nil {
		return fmt.Errorf("address book update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("tag management not supported by this api server")
	}
	return nil
}

func apiLogin(base, username, password string) (string, error) {
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		return "", fmt.Errorf("encode login request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/login", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := apiClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("api login: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read login response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("api login failed: status %d", resp.StatusCode)
	}
	var lr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &lr); err != nil {
		return "", fmt.Errorf("decode login response: %w", err)
	}
	if lr.AccessToken == "" {
		return "", fmt.Errorf("api login response missing access_token")
	}
	return lr.AccessToken, nil
}

func apiGetAB(base, token string) ([]apiEntry, error) {
	req, err := http.NewRequest(http.MethodGet, base+"/api/ab", nil)
	if err != nil {
		return nil, fmt.Errorf("build address book request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := apiClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("address book: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read address book response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("address book failed: status %d", resp.StatusCode)
	}
	var entries []apiEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		var wrapped struct {
			Data []apiEntry `json:"data"`
		}
		if err2 := json.Unmarshal(data, &wrapped); err2 != nil || wrapped.Data == nil {
			return nil, fmt.Errorf("decode address book: %w", err)
		}
		entries = wrapped.Data
	}
	return entries, nil
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

func CheckOnlineBatch(server string, peerIDs []string, timeout time.Duration) (map[string]bool, error) {
	res, _, err := QueryOnline(server, peerIDs, timeout)
	return res, err
}

func BatchOnline(server string, ids []string, timeout time.Duration) (map[string]bool, error) {
	return CheckOnlineBatch(server, ids, timeout)
}
