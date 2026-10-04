package mounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// A Hugging Face Hub mount: a dataset or model repository read at one commit. The tree API lists files
// (paginated through the Link header), resolve/<revision>/<path> serves one. Source: huggingface_hub HfApi
// list_repo_tree and hf_hub_url (the Hub's HTTP API).

type hfReader struct {
	base, repo, revision, token string
	client                      *http.Client
}

// repoAPI is "datasets/<org>/<name>" → ("datasets", "<org>/<name>"); a bare "<org>/<model>" is a model.
func (r *hfReader) repoAPI() (string, string) {
	for _, t := range []string{"datasets", "spaces"} {
		if rest, ok := strings.CutPrefix(r.repo, t+"/"); ok {
			return t, rest
		}
	}
	return "models", r.repo
}

func (r *hfReader) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hub %s: %w", r.repo, err)
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("hub %s: %s: %s", r.repo, resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

type hfEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func (r *hfReader) Walk(ctx context.Context, prefix string, fn func(string, int64) error) error {
	t, repo := r.repoAPI()
	u := r.base + "/api/" + t + "/" + repo + "/tree/" + url.PathEscape(r.revision)
	if prefix != "" {
		if err := checkPath(prefix); err != nil {
			return err
		}
		u += "/" + escapeSegments(prefix)
	}
	u += "?recursive=true&expand=false"
	for u != "" {
		resp, err := r.get(ctx, u)
		if err != nil {
			return err
		}
		var page []hfEntry
		err = json.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(&page)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("hub %s: list: %w", r.repo, err)
		}
		for _, e := range page {
			if e.Type != "file" {
				continue
			}
			if err := fn(e.Path, e.Size); err != nil {
				if errors.Is(err, ErrStop) {
					return nil
				}
				return err
			}
		}
		u = nextLink(resp.Header.Get("Link"))
	}
	return nil
}

func (r *hfReader) Open(ctx context.Context, rel string) (io.ReadCloser, error) {
	if err := checkPath(rel); err != nil {
		return nil, err
	}
	t, repo := r.repoAPI()
	p := repo
	if t != "models" {
		p = t + "/" + repo
	}
	resp, err := r.get(ctx, r.base+"/"+p+"/resolve/"+url.PathEscape(r.revision)+"/"+escapeSegments(rel))
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func escapeSegments(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// nextLink reads rel="next" from a Link header ("" when there is none).
func nextLink(h string) string {
	for _, part := range strings.Split(h, ",") {
		u, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if ok && strings.Contains(params, `rel="next"`) {
			return strings.Trim(strings.TrimSpace(u), "<>")
		}
	}
	return ""
}
