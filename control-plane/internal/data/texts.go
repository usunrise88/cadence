package data

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// TextKindDatasetCard is the one kind of text texts.get serves: a blob a registry version names as text meant to be
// read (a dataset card).
const TextKindDatasetCard = "dataset-card"

// TextLimit caps a text texts.get serves; a longer one comes back truncated at a rune boundary.
const TextLimit = 256 << 10

// Text is a text of the content store a registry version names to be read (the contract's RegistryText).
type Text struct {
	Hash       string `json:"hash"`
	Kind       string `json:"kind"`
	MediaType  string `json:"mediaType"`
	Text       string `json:"text"`
	Bytes      int64  `json:"bytes"`
	Truncated  bool   `json:"truncated"`
	VersionID  string `json:"versionId"`
	Collection string `json:"collection,omitempty"`
	Version    string `json:"version,omitempty"`
}

// ReadText reads blob hash as text when a registry version names it as text to read — a dataset version's card
// (payload card.hash) — and refuses (not-found) any other hash, whatever the blob holds: audio, manifests and shards
// are read by steps, never served as text. The caller checks registry read.
func ReadText(ctx context.Context, q storage.Querier, store *cas.Store, hash string) (Text, error) {
	t := Text{Hash: hash, Kind: TextKindDatasetCard, MediaType: "text/markdown"}
	err := q.QueryRow(ctx, `SELECT v.id, c.name, v.version FROM registry_versions v
		JOIN registry_collections c ON c.id = v.collection_id
		WHERE c.kind = $1 AND v.payload->'card'->>'hash' = $2 ORDER BY v.created_at, v.id LIMIT 1`,
		registry.KindDataset, hash).Scan(&t.VersionID, &t.Collection, &t.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Text{}, problems.NotFound.New("no registry version names %s as a text to read (a dataset card); artifacts.get reads artifacts", hash)
	}
	if err != nil {
		return Text{}, fmt.Errorf("find the version that names %s: %w", hash, err)
	}
	if store == nil {
		return Text{}, errors.New("data: no content store to read texts from")
	}
	f, err := store.Open(hash)
	if err != nil {
		return Text{}, problems.ArtifactMissing.New("the card of %s %s (%s) is not in the content store: %v", t.Collection, t.Version, t.VersionID, err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, TextLimit+1))
	if err != nil {
		return Text{}, fmt.Errorf("read %s: %w", hash, err)
	}
	if st, err := f.Stat(); err == nil {
		t.Bytes = st.Size()
	} else {
		t.Bytes = int64(len(b))
	}
	t.Text, t.Truncated = TruncateText(b, TextLimit)
	return t, nil
}

// TruncateText returns b as text cut to at most limit bytes at a rune boundary, and whether it was cut; invalid UTF-8
// becomes U+FFFD.
func TruncateText(b []byte, limit int) (string, bool) {
	cut := len(b) > limit
	if cut {
		b = b[:limit]
		for i := 0; i < utf8.UTFMax-1 && len(b) > 0; i++ {
			if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size != 1 {
				break
			}
			b = b[:len(b)-1]
		}
	}
	return strings.ToValidUTF8(string(b), "�"), cut
}
