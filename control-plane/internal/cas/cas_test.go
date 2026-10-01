package cas

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestPutOpenManifest(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// BLAKE3-256 of the empty input (the reference test vector).
	if got := Hash(nil); got != "b3:af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262" {
		t.Fatalf("Hash(nil) = %s", got)
	}
	h, n, err := s.Put(strings.NewReader("hello"), "")
	if err != nil || n != 5 || h != Hash([]byte("hello")) {
		t.Fatalf("Put = %s %d %v", h, n, err)
	}
	if again, _, err := s.Put(strings.NewReader("hello"), h); err != nil || again != h {
		t.Fatalf("second Put = %s %v", again, err)
	}
	if _, _, err := s.Put(strings.NewReader("other"), h); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("mismatch err = %v", err)
	}
	if ok, size, err := s.Has(h); !ok || size != 5 || err != nil {
		t.Fatalf("Has = %v %d %v", ok, size, err)
	}
	f, err := s.Open(h)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 5)
	_, _ = f.Read(b)
	_ = f.Close()
	if string(b) != "hello" {
		t.Fatalf("read %q", b)
	}
	if _, err := s.Open(Hash([]byte("absent"))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent err = %v", err)
	}
	if entries, _ := os.ReadDir(s.Root() + "/tmp"); len(entries) != 0 {
		t.Fatalf("tmp not empty: %v", entries)
	}

	w, _ := s.PutBytes([]byte("world"))
	m1, err := s.PutManifest(Manifest{Files: []File{{Path: "b/w.txt", Hash: w, Size: 5}, {Path: "a.txt", Hash: h, Size: 5}}})
	if err != nil {
		t.Fatal(err)
	}
	m2, _ := s.PutManifest(Manifest{Files: []File{{Path: "a.txt", Hash: h, Size: 5}, {Path: "b/w.txt", Hash: w, Size: 5}}})
	if m1 != m2 {
		t.Fatalf("manifest hash depends on order: %s %s", m1, m2)
	}
	m, err := s.ReadManifest(m1)
	if err != nil || len(m.Files) != 2 || m.Files[0].Path != "a.txt" {
		t.Fatalf("ReadManifest = %+v %v", m, err)
	}
	for _, bad := range []string{"../x", "/abs", "a//b", "./a"} {
		if _, err := s.PutManifest(Manifest{Files: []File{{Path: bad, Hash: h}}}); err == nil {
			t.Errorf("path %q accepted", bad)
		}
	}
	if _, err := s.PutManifest(Manifest{Files: []File{{Path: "x", Hash: Hash([]byte("absent"))}}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file err = %v", err)
	}
}

func TestDelete(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.PutBytes([]byte("state"))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.Delete(h); err != nil || n != 5 {
		t.Fatalf("Delete = %d %v", n, err)
	}
	if ok, _, _ := s.Has(h); ok {
		t.Fatal("the blob is still there")
	}
	if n, err := s.Delete(h); err != nil || n != 0 {
		t.Fatalf("second Delete = %d %v", n, err)
	}
	if _, err := s.Delete("nope"); err == nil {
		t.Fatal("a malformed hash deletes nothing and says so")
	}
}
