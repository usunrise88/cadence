//go:build integration

package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/registry"
)

// TestTextsGet: texts.get serves a dataset version's card from the content store (registry read, capped); a blob no
// registry version names as text — audio, a manifest — is not found, whatever it holds.
func TestTextsGet(t *testing.T) {
	e, store := startData(t)
	ctx := context.Background()
	put := func(b []byte) string {
		h, err := store.PutBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	card := "# Dataset card\n\n- Source: `fleurs-sr` (CC-BY-4.0)\n- Hours: 2.0 — ćevapi\n"
	small := put([]byte(card))
	big := put([]byte(strings.Repeat("ž", 200<<10))) // 400 KiB of two-byte runes
	audio := put([]byte("RIFF-fake-audio"))
	for i, h := range []string{small, big} {
		err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			_, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindDataset, Name: fmt.Sprintf("dataset/card-%d", i),
				Actor: registry.Bundled(), Freeze: true,
				Payload: []byte(fmt.Sprintf(`{"artifact":{"hash":%q,"type":"dataset"},"hours":0.1,"card":{"hash":%q,"bytes":10}}`, audio, h))}, time.Now())
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	var got struct {
		Hash, Kind, MediaType, Text, VersionID, Collection, Version string
		Bytes                                                       int64
		Truncated                                                   bool
	}
	e.ok(e.do("GET", "/api/registry/texts/"+small, ""), 200, &got)
	if got.Text != card || got.Kind != "dataset-card" || got.MediaType != "text/markdown" || got.Truncated || got.Bytes != int64(len(card)) ||
		got.Collection != "dataset/card-0" || !strings.HasPrefix(got.VersionID, "ver_") {
		t.Fatalf("card %+v", got)
	}
	e.ok(e.agent("GET", "/api/registry/texts/"+small, ""), 200, &got) // an agent reads the registry too

	e.ok(e.do("GET", "/api/registry/texts/"+big, ""), 200, &got)
	if !got.Truncated || got.Bytes != 400<<10 || len(got.Text) != 256<<10 || !strings.HasSuffix(got.Text, "ž") {
		t.Fatalf("big card: truncated %v, %d bytes, %d served", got.Truncated, got.Bytes, len(got.Text))
	}

	expectProblem(t, e.do("GET", "/api/registry/texts/"+audio, ""), 404, "not-found")
	expectProblem(t, e.do("GET", "/api/registry/texts/b3:"+strings.Repeat("0", 64), ""), 404, "not-found")
}
