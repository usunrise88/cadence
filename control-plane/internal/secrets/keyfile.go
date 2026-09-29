package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// KeySize is the secretbox key length.
const KeySize = 32

// LoadOrCreateKey reads the master key at path, or generates one there when the file does not exist (created is
// then true, and the caller warns: whoever loses the file loses every stored secret). The file holds the key as
// base64 (what this function writes), hex, or 32 raw bytes.
func LoadOrCreateKey(path string) (key *[KeySize]byte, created bool, err error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the operator chooses the key path (CADENCE_MASTER_KEY_FILE)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		key = new([KeySize]byte)
		if _, err := rand.Read(key[:]); err != nil {
			return nil, false, fmt.Errorf("generate master key: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, false, fmt.Errorf("create master key directory: %w", err)
		}
		enc := base64.StdEncoding.EncodeToString(key[:]) + "\n"
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // see above
		if err != nil {
			return nil, false, fmt.Errorf("create master key %s: %w", path, err)
		}
		if _, err := f.WriteString(enc); err != nil {
			_ = f.Close()
			return nil, false, fmt.Errorf("write master key %s: %w", path, err)
		}
		if err := f.Close(); err != nil {
			return nil, false, fmt.Errorf("write master key %s: %w", path, err)
		}
		return key, true, nil
	case err != nil:
		return nil, false, fmt.Errorf("read master key %s: %w", path, err)
	}
	key, err = parseKey(raw)
	if err != nil {
		return nil, false, fmt.Errorf("master key %s: %w", path, err)
	}
	return key, false, nil
}

func parseKey(raw []byte) (*[KeySize]byte, error) {
	var key [KeySize]byte
	if len(raw) == KeySize {
		copy(key[:], raw)
		return &key, nil
	}
	text := string(bytes.TrimSpace(raw))
	for _, dec := range []func(string) ([]byte, error){base64.StdEncoding.DecodeString, hex.DecodeString} {
		if b, err := dec(text); err == nil && len(b) == KeySize {
			copy(key[:], b)
			return &key, nil
		}
	}
	return nil, fmt.Errorf("must hold %d bytes as base64, hex or raw", KeySize)
}
