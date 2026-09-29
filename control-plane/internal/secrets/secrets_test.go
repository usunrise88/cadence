package secrets

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "master.key")
	key, created, err := LoadOrCreateKey(path)
	if err != nil || !created {
		t.Fatalf("first start: created=%v err=%v", created, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v, want 0600", info.Mode().Perm())
	}
	again, created, err := LoadOrCreateKey(path)
	if err != nil || created || *again != *key {
		t.Fatalf("second start: created=%v err=%v same=%v", created, err, *again == *key)
	}

	for name, content := range map[string][]byte{
		"hex": []byte(hex.EncodeToString(key[:]) + "\n"),
		"raw": key[:],
	} {
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, content, 0o600); err != nil {
			t.Fatal(err)
		}
		got, _, err := LoadOrCreateKey(p)
		if err != nil || *got != *key {
			t.Errorf("%s key: err=%v", name, err)
		}
	}
	bad := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(bad, []byte("too short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreateKey(bad); err == nil {
		t.Error("a malformed key was accepted")
	}
}

func TestSealedFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	key := &[KeySize]byte{7}
	s, err := NewStore(nil, dir, key)
	if err != nil {
		t.Fatal(err)
	}
	const id = "sec_0192f5c4-3b1e-7c3a-9d4e-0123456789ab"
	value := []byte("hf_very-secret-token")
	if err := s.write(id, value); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, value) {
		t.Fatal("the value is stored in plain text")
	}
	info, _ := os.Stat(filepath.Join(dir, id))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("value file mode %v, want 0600", info.Mode().Perm())
	}
	if dirInfo, _ := os.Stat(dir); dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("store directory mode %v, want 0700", dirInfo.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("store holds %d files, want 1 (no temp files left)", len(entries))
	}
	got, err := s.read(id)
	if err != nil || !bytes.Equal(got, value) {
		t.Fatalf("read = %q, %v", got, err)
	}

	other, _ := NewStore(nil, dir, &[KeySize]byte{8})
	if _, err := other.read(id); err == nil || !strings.Contains(err.Error(), "cannot decrypt") {
		t.Errorf("read with the wrong key = %v", err)
	}
	if err := s.write("../escape", value); err == nil {
		t.Error("a malformed id was accepted as a file name")
	}
	first, second := s.RequestMAC(value), s.RequestMAC(value)
	if first == other.RequestMAC(value) || first != second {
		t.Error("RequestMAC must be deterministic per key and differ between keys")
	}
}
