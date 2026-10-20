package config

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestSaveCreatesPrivateConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	t.Setenv("RDC_CONFIG", path)
	cfg := &Config{
		Server:      "fixture.example:21116",
		Key:         "fixture-key",
		AccessToken: "fixture-token",
		APIServer:   "https://fixture.example",
		APIUsername: "fixture-user",
		APIPassword: "fixture-\"password\"\n\t\\",
		Peers: map[string]Peer{
			"42": {Name: "fixture peer", Password: "fixture-peer-password"},
		},
		Tags: map[string][]string{"42": {"dev", "work"}},
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	assertPrivateConfigFile(t, path)
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, cfg) {
		t.Fatalf("config did not round-trip: got %#v, want %#v", loaded, cfg)
	}
	assertConfigDirectoryEntries(t, filepath.Dir(path), "config.toml")
}

func TestSaveReplacesExistingConfigPrivately(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission and open-file replacement semantics")
	}
	for _, mode := range []os.FileMode{0o644, 0o666, 0o400, 0o600} {
		t.Run(mode.String(), func(t *testing.T) {
			original := "access_token = \"old-fixture-token\"\n"
			path := writeTempConfig(t, original)
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			oldFile, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer oldFile.Close()
			cfg := &Config{AccessToken: "new-fixture-token"}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			assertPrivateConfigFile(t, path)
			oldData, err := io.ReadAll(oldFile)
			if err != nil {
				t.Fatal(err)
			}
			if string(oldData) != original {
				t.Fatalf("Save modified the original inode: %q", oldData)
			}
			loaded, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if loaded.AccessToken != cfg.AccessToken {
				t.Fatalf("saved token = %q", loaded.AccessToken)
			}
			assertConfigDirectoryEntries(t, filepath.Dir(path), "config.toml")
		})
	}
}

func TestSaveRejectsSymlink(t *testing.T) {
	for _, dangling := range []bool{false, true} {
		name := "existing"
		if dangling {
			name = "dangling"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			target := filepath.Join(dir, "target.toml")
			original := "fixture target contents\n"
			if !dangling {
				if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink("target.toml", path); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("symlinks unavailable: %v", err)
				}
				t.Fatal(err)
			}
			t.Setenv("RDC_CONFIG", path)
			if err := (&Config{AccessToken: "fixture-token"}).Save(); err == nil {
				t.Fatal("Save accepted a symlink destination")
			}
			if got, err := os.Readlink(path); err != nil || got != "target.toml" {
				t.Fatalf("symlink changed: target=%q, err=%v", got, err)
			}
			data, err := os.ReadFile(target)
			if dangling {
				if !os.IsNotExist(err) {
					t.Fatalf("dangling target was created: %v", err)
				}
				assertConfigDirectoryEntries(t, dir, "config.toml")
			} else {
				if err != nil || string(data) != original {
					t.Fatalf("target changed: contents=%q, err=%v", data, err)
				}
				assertConfigDirectoryEntries(t, dir, "config.toml", "target.toml")
			}
		})
	}
}

func TestSaveFailurePreservesOriginal(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions without root privileges")
	}
	original := "access_token = \"original-fixture-token\"\n"
	path := writeTempConfig(t, original)
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Error(err)
		}
	})
	if err := (&Config{AccessToken: "replacement-fixture-token"}).Save(); err == nil {
		t.Fatal("Save succeeded in an unwritable directory")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != original {
		t.Fatalf("failed Save changed original: contents=%q, err=%v", data, err)
	}
	assertConfigDirectoryEntries(t, dir, "config.toml")
}

func TestSaveRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RDC_CONFIG", path)
	if err := (&Config{AccessToken: "fixture-token"}).Save(); err == nil {
		t.Fatal("Save accepted a directory destination")
	}
	assertConfigDirectoryEntries(t, dir, "config.toml")
	assertConfigDirectoryEntries(t, path)
}

func TestSaveFailedReplacementRemovesTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	t.Setenv("RDC_CONFIG", path+string(os.PathSeparator))
	err := (&Config{AccessToken: "fixture-token"}).Save()
	if err == nil || !strings.Contains(err.Error(), "config: replace ") {
		t.Fatalf("expected replacement failure, got %v", err)
	}
	assertConfigDirectoryEntries(t, dir, "config.toml")
	assertConfigDirectoryEntries(t, path)
}

func assertPrivateConfigFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("config mode = %s, want a regular file", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}
}

func assertConfigDirectoryEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("directory entries = %v, want %v", got, want)
	}
}
