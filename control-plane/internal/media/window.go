package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Audio indexed in place (phase 4 · stream A): segments of files on a mount that were never copied into the content
// store — a draft dataset version's utterances, an annotation batch item's window, a triage item's window. The control
// plane sees local, NFS and SMB mounts at the same path as the workers (compose binds them), so it reads the file and
// serves the window like a stored WAV: PCM, float or G.711 (the telephone calls); other codecs are served once a
// dataset version is frozen.

// Window is a span of a file on a mount.
type Window struct {
	File  string  // mount://<mount>/<path>
	Start float64 // seconds
	End   float64
	// Only is the channel that is the audio's own (a segment with &ch=n), nil for every channel.
	Only *int
}

// Key names the window's derived media (its peaks are cached under it as meta.audio).
func (w Window) Key() string {
	return w.File + "#t=" + strconv.FormatFloat(w.Start, 'f', 3, 64) + "," + strconv.FormatFloat(w.End, 'f', 3, 64)
}

// windowContext is how much of the source file a triage item's window adds on each side of its segment (seconds).
const windowContext = 2.0

func lookupItem(ctx context.Context, q storage.Querier, id string) (Utterance, error) {
	var (
		raw, seg []byte
		u        = Utterance{ID: id, Target: -1}
	)
	err := q.QueryRow(ctx, `SELECT i.audio_window, i.segment, b.id, b.project_id FROM annotation_items i
		JOIN annotation_batches b ON b.id = i.batch_id WHERE i.id = $1`, id).Scan(&raw, &seg, &u.BatchID, &u.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Utterance{}, problems.NotFound.New("no annotation item %q", id)
	}
	if err != nil {
		return Utterance{}, fmt.Errorf("look up annotation item %s: %w", id, err)
	}
	var w struct {
		File     string   `json:"file"`
		Start    float64  `json:"start"`
		End      float64  `json:"end"`
		Channels int      `json:"channels"`
		Roles    []string `json:"roles"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return Utterance{}, fmt.Errorf("decode the window of %s: %w", id, err)
	}
	var s struct {
		Channel *int `json:"channel"`
	}
	_ = json.Unmarshal(seg, &s)
	if s.Channel != nil {
		u.Target = *s.Channel
	}
	win := Window{File: w.File, Start: w.Start, End: w.End}
	u.Window, u.Hash, u.Duration, u.Channels, u.Roles = &win, win.Key(), w.End-w.Start, w.Channels, w.Roles
	return u, nil
}

func lookupTriage(ctx context.Context, q storage.Querier, id string) (Utterance, error) {
	var (
		seg struct {
			URI     string   `json:"uri"`
			Start   *float64 `json:"start"`
			End     *float64 `json:"end"`
			Channel *int     `json:"channel"`
		}
		u = Utterance{ID: id, Target: -1}
	)
	err := q.QueryRow(ctx, "SELECT segment, project_id FROM triage_items WHERE id = $1", id).Scan(&seg, &u.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Utterance{}, problems.NotFound.New("no triage item %q", id)
	}
	if err != nil {
		return Utterance{}, fmt.Errorf("look up triage item %s: %w", id, err)
	}
	ref, err := mounts.ParseURI(seg.URI)
	if err != nil {
		return Utterance{}, problems.BadRequest.New("triage item %s has no mount URI to play: %v", id, err)
	}
	start, end := seg.Start, seg.End
	if start == nil || end == nil {
		start, end = ref.Start, ref.End
	}
	if start == nil || end == nil {
		return Utterance{}, problems.BadRequest.New("triage item %s names no time range of its file", id)
	}
	if seg.Channel != nil {
		u.Target = *seg.Channel
	} else if ref.Channel != nil {
		u.Target = *ref.Channel
	}
	ref.Start, ref.End, ref.Channel = nil, nil, nil
	win := Window{File: ref.String(), Start: math.Max(0, *start-windowContext), End: *end + windowContext}
	u.Window, u.Hash, u.Duration = &win, win.Key(), win.End-win.Start
	return u, nil
}

// segmentWindow is the window an utterance's mount URI names (its own channel only).
func (s *Service) segmentWindow(uri string) (Window, error) {
	ref, err := mounts.ParseURI(uri)
	if err != nil {
		return Window{}, problems.BadRequest.New("the utterance's mount URI %q: %v", uri, err)
	}
	w := Window{Start: 0, End: math.Inf(1), Only: ref.Channel}
	if ref.Start != nil && ref.End != nil {
		w.Start, w.End = *ref.Start, *ref.End
	}
	ref.Start, ref.End, ref.Channel = nil, nil, nil
	w.File = ref.String()
	return w, nil
}

// localPath resolves a mount URI to the file the control plane sees: path-kind mounts only.
func (s *Service) localPath(ctx context.Context, uri string) (string, error) {
	ref, err := mounts.ParseURI(uri)
	if err != nil {
		return "", problems.BadRequest.New("%v", err)
	}
	if s.Pool == nil {
		return "", problems.NotImplemented.New("this control plane reads no mounts")
	}
	var kind, root string
	err = s.Pool.QueryRow(ctx, "SELECT kind, root FROM mounts WHERE name = $1", ref.Mount).Scan(&kind, &root)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", problems.NotFound.New("no mount %q", ref.Mount)
	}
	if err != nil {
		return "", fmt.Errorf("read mount %s: %w", ref.Mount, err)
	}
	if !mounts.PathKind(kind) {
		return "", problems.Conflict.New("mount %s is %s: its audio is served once a dataset version is frozen (the control plane plays local, NFS and SMB mounts in place)",
			ref.Mount, kind)
	}
	return filepath.Join(root, filepath.FromSlash(path.Clean("/"+ref.Path))), nil
}

// openWindow opens the window's file on its mount and answers its audio as an Info of the window.
func (s *Service) openWindow(u Utterance, w Window) (*os.File, Info, error) {
	p, err := s.localPath(context.Background(), w.File)
	if err != nil {
		return nil, Info{}, err
	}
	f, err := os.Open(p) //nolint:gosec // a mount root joined with a cleaned relative path
	if errors.Is(err, os.ErrNotExist) {
		return nil, Info{}, problems.NotFound.New("%s is not on its mount any more (the audio of %s)", w.File, u.ID)
	}
	if err != nil {
		return nil, Info{}, fmt.Errorf("open %s: %w", w.File, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, Info{}, fmt.Errorf("stat %s: %w", w.File, err)
	}
	info, err := ReadInfo(f, st.Size())
	if err != nil {
		_ = f.Close()
		ext := strings.TrimPrefix(path.Ext(w.File), ".")
		return nil, Info{}, problems.ValidationFailed.New("%s cannot be played in place (%v; %s): freeze its dataset version to hear it", w.File, err, ext)
	}
	end := w.End
	if math.IsInf(end, 1) {
		end = info.Duration()
	}
	win := info.Window(w.Start, end)
	if w.Only != nil && *w.Only < info.Channels {
		ch := *w.Only
		win.Only = &ch
	}
	return f, win, nil
}
