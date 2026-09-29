// Command helpsync mirrors docs/help into internal/help/content, the copy go:embed bundles into the binary.
// Files missing from the source are deleted from the mirror. Run it from control-plane/ after editing docs/help:
//
//	go run ./cmd/helpsync
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	src := flag.String("src", "../docs/help", "help articles written by people")
	dst := flag.String("dst", "internal/help/content", "generated mirror embedded in the binary")
	flag.Parse()
	changed, err := mirror(*src, *dst)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helpsync:", err)
		os.Exit(1)
	}
	fmt.Printf("helpsync: %d file(s) changed in %s\n", changed, *dst)
}

func mirror(src, dst string) (int, error) {
	changed := 0
	want := map[string]bool{}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		want[rel] = true
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if old, err := os.ReadFile(target); err == nil && bytes.Equal(old, data) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		changed++
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		return changed, fmt.Errorf("copy %s: %w", src, err)
	}
	err = filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dst, p)
		if err != nil || want[rel] {
			return err
		}
		changed++
		return os.Remove(p)
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return changed, fmt.Errorf("prune %s: %w", dst, err)
	}
	return changed, nil
}
