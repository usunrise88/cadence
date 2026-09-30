package secrets

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestTransit: a value in transit is sealed 0600 under its task id, reads back, is listed for the sweeper and is gone
// after DropTransit; malformed names never touch the file system.
func TestTransit(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(nil, dir, &[KeySize]byte{7})
	if err != nil {
		t.Fatal(err)
	}
	name := "act_01923456-7890-7abc-8def-0123456789ab"
	value := []byte("sk-ant-oat01-secret-value")
	if err := s.SealTransit(name, value); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "transit", name)
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("transit file mode %v, want 0600", st.Mode().Perm())
	}
	raw, _ := os.ReadFile(p)
	if bytes.Contains(raw, value) {
		t.Error("the transit file holds the value in the clear")
	}
	got, err := s.OpenTransit(name)
	if err != nil || !bytes.Equal(got, value) {
		t.Fatalf("OpenTransit = %q, %v", got, err)
	}
	names, err := s.TransitNames()
	if err != nil || len(names) != 1 {
		t.Fatalf("TransitNames = %v, %v", names, err)
	}
	if err := s.DropTransit(name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenTransit(name); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after DropTransit: %v, want not exist", err)
	}
	if err := s.DropTransit(name); err != nil {
		t.Errorf("dropping twice: %v", err)
	}
	for _, bad := range []string{"../master.key", "sec_01923456-7890-7abc-8def-0123456789ab", "act_x"} {
		if err := s.SealTransit(bad, value); err == nil {
			t.Errorf("SealTransit(%q) accepted", bad)
		}
	}
}
