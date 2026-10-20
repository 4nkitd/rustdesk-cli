package hbb

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/4nkitd/rustdesk-cli/internal/config"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func TestLocalSourceListSorted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RDC_CONFIG", filepath.Join(dir, "config.toml"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	cfg.AddTag("9", "z")
	cfg.AddTag("1", "b")
	cfg.AddTag("1", "a")
	cfg.AddTag("5", "x")

	src := LocalSource(cfg)
	devs, err := src.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	want := []string{"1", "5", "9"}
	got := make([]string, len(devs))
	for i, d := range devs {
		got[i] = d.ID
		if d.Source != "local" {
			t.Fatalf("device %s source got %q", d.ID, d.Source)
		}
		if d.Name != "" || d.Platform != "" {
			t.Fatalf("device %s should have empty name/platform", d.ID)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List order got %v, want %v", got, want)
	}
	if tags := src.Tags("1"); !reflect.DeepEqual(tags, []string{"a", "b"}) {
		t.Fatalf("Tags(1) got %v", tags)
	}
}

func TestLocalSourceAddRemoveTagPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	t.Setenv("RDC_CONFIG", path)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	src := LocalSource(cfg)
	if err := src.AddTag("7", "work"); err != nil {
		t.Fatalf("AddTag failed: %v", err)
	}
	if err := src.AddTag("7", "prod"); err != nil {
		t.Fatalf("AddTag 2 failed: %v", err)
	}
	if got := src.Tags("7"); !reflect.DeepEqual(got, []string{"prod", "work"}) {
		t.Fatalf("Tags(7) got %v", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config not saved: %v", err)
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if got := reloaded.TagsFor("7"); !reflect.DeepEqual(got, []string{"prod", "work"}) {
		t.Fatalf("reloaded TagsFor(7) got %v", got)
	}

	rsrc := LocalSource(reloaded)
	if err := rsrc.RemoveTag("7", "prod"); err != nil {
		t.Fatalf("RemoveTag failed: %v", err)
	}
	if got := rsrc.Tags("7"); !reflect.DeepEqual(got, []string{"work"}) {
		t.Fatalf("Tags(7) after remove got %v", got)
	}
	if got := reloaded.TagsFor("7"); !reflect.DeepEqual(got, []string{"work"}) {
		t.Fatalf("reloaded TagsFor(7) after remove got %v", got)
	}
}

func TestCheckOnlineBatchBitDecoding(t *testing.T) {
	peers := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	for attempt := 0; listener.Addr().(*net.TCPAddr).Port == 65535; attempt++ {
		listener.Close()
		if attempt == 15 {
			t.Fatal("could not reserve an online endpoint below port 65535")
		}
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("Listen failed: %v", err)
		}
	}
	defer listener.Close()
	serverPort := listener.Addr().(*net.TCPAddr).Port + 1
	serverAddr := fmt.Sprintf("127.0.0.1:%d", serverPort)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		msg, err := readRendezvousMessage(conn)
		if err != nil {
			return
		}
		olReq := msg.GetOnlineRequest()
		if olReq == nil {
			return
		}
		got := olReq.GetPeers()
		if !reflect.DeepEqual(got, peers) {
			t.Errorf("batch peers got %v, want %v", got, peers)
			return
		}

		// peers 0, 3, 8 online -> byte0 = 0x80|0x10 = 0x90, byte1 = 0x80
		states := []byte{0x90, 0x80}
		resp := &pb.RendezvousMessage{
			Union: &pb.RendezvousMessage_OnlineResponse{
				OnlineResponse: &pb.OnlineResponse{States: states},
			},
		}
		_ = writeRendezvousMessage(conn, resp)
	}()

	result, err := CheckOnlineBatch(serverAddr, peers, 2*time.Second)
	if err != nil {
		t.Fatalf("CheckOnlineBatch failed: %v", err)
	}
	want := map[string]bool{
		"a": true, "b": false, "c": false, "d": true, "e": false,
		"f": false, "g": false, "h": false, "i": true, "j": false,
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("batch result got %v, want %v", result, want)
	}
}

func TestCheckOnlineBatchEmpty(t *testing.T) {
	result, err := CheckOnlineBatch("127.0.0.1:1", nil, time.Second)
	if err != nil {
		t.Fatalf("empty batch failed: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty result, got %v", result)
	}
}

func TestAPISource(t *testing.T) {
	var putCount int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("login method got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok123"}`))
	})
	mux.HandleFunc("/api/ab", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"1","name":"office","platform":"linux","tags":["work"]}]`))
		case http.MethodPut:
			putCount++
			if r.Header.Get("Authorization") != "Bearer tok123" {
				t.Errorf("put auth got %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`ok`))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &config.Config{APIServer: srv.URL, APIUsername: "alice", APIPassword: "pw"}
	src, err := APISource(cfg)
	if err != nil {
		t.Fatalf("APISource failed: %v", err)
	}

	devs, err := src.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(devs) != 1 || devs[0].ID != "1" || devs[0].Name != "office" || devs[0].Platform != "linux" {
		t.Fatalf("List got %+v", devs)
	}
	if devs[0].Source != "api" {
		t.Fatalf("source got %q", devs[0].Source)
	}
	if tags := src.Tags("1"); !reflect.DeepEqual(tags, []string{"work"}) {
		t.Fatalf("Tags(1) got %v", tags)
	}

	if err := src.AddTag("1", "prod"); err != nil {
		t.Fatalf("AddTag failed: %v", err)
	}
	if putCount != 1 {
		t.Fatalf("expected 1 PUT, got %d", putCount)
	}
	if got := src.Tags("1"); !reflect.DeepEqual(got, []string{"prod", "work"}) {
		t.Fatalf("Tags(1) after add got %v", got)
	}

	if err := src.AddTag("99", "x"); err == nil {
		t.Fatalf("expected unknown device error")
	}

	if err := src.RemoveTag("1", "prod"); err != nil {
		t.Fatalf("RemoveTag failed: %v", err)
	}
	if got := src.Tags("1"); !reflect.DeepEqual(got, []string{"work"}) {
		t.Fatalf("Tags(1) after remove got %v", got)
	}
}

func TestAPISourceUnsupportedTagManagement(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok123"}`))
	})
	mux.HandleFunc("/api/ab", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"1","name":"n","platform":"p","tags":[]}]`))
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &config.Config{APIServer: srv.URL, APIUsername: "alice", APIPassword: "pw"}
	src, err := APISource(cfg)
	if err != nil {
		t.Fatalf("APISource failed: %v", err)
	}
	err = src.AddTag("1", "work")
	if err == nil {
		t.Fatalf("expected unsupported error")
	}
	if err.Error() != "tag management not supported by this api server" {
		t.Fatalf("got error %q", err.Error())
	}
}

func TestNewDeviceSource(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RDC_CONFIG", filepath.Join(dir, "config.toml"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	src, err := NewDeviceSource(cfg)
	if err != nil {
		t.Fatalf("NewDeviceSource failed: %v", err)
	}
	if _, ok := src.(*localSource); !ok {
		t.Fatalf("expected localSource, got %T", src)
	}
}
