package annotation

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// TextLimit caps the guidelines text guidelines.get serves (the contract's GuidelinesText): longer files come back
// truncated at a rune boundary.
const TextLimit = 256 << 10

// GuidelinesText is a batch's guidelines file at its pinned commit (the contract's GuidelinesText).
type GuidelinesText struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Commit    string `json:"commit"`
	Text      string `json:"text"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated"`
}

// Guidelines reads the guidelines batch id pinned (R27): annotation/guidelines/<name>.md at the commit the batch
// recorded, never main today, so an annotator reads what the batch is annotated against. The caller checks who may
// read the batch (a reviewer of that batch, or the project).
func (s *Service) Guidelines(ctx context.Context, q storage.Querier, id string) (GuidelinesText, error) {
	b, err := getBatch(ctx, q, id, false)
	if err != nil {
		return GuidelinesText{}, err
	}
	g := b.Guidelines
	if s.Repo == nil {
		return GuidelinesText{}, problems.RepositoryUnavailable.New("this control plane reaches no project repositories; the guidelines of batch %s cannot be read", id)
	}
	if g.Path == "" || g.Commit == "" {
		return GuidelinesText{}, problems.NotFound.New("batch %s pinned no guidelines", id)
	}
	var slug string
	err = q.QueryRow(ctx, "SELECT slug FROM projects WHERE id = $1", b.ProjectID).Scan(&slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return GuidelinesText{}, problems.NotFound.New("batch %s: its project %s is gone", id, b.ProjectID)
	}
	if err != nil {
		return GuidelinesText{}, fmt.Errorf("read the project of batch %s: %w", id, err)
	}
	content, _, err := s.Repo.ReadFile(ctx, slug, g.Commit, g.Path)
	if err != nil {
		return GuidelinesText{}, problems.NotFound.New("%s is not at commit %.12s of the project repository (was the history rewritten?)", g.Path, g.Commit)
	}
	text, cut := data.TruncateText(content, TextLimit)
	return GuidelinesText{Name: g.Name, Path: g.Path, Commit: g.Commit, Text: text, Bytes: int64(len(content)), Truncated: cut}, nil
}
