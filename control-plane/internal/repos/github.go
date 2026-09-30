package repos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GitHub creates repositories through the GitHub REST API (docs.github.com/en/rest/repos/repos#create-a-repository-
// for-the-authenticated-user and …#create-an-organization-repository).
type GitHub struct {
	// BaseURL is the API root; https://api.github.com when empty (GitHub Enterprise: https://<host>/api/v3).
	BaseURL string
	// HTTP is the client; a 30-second client when nil.
	HTTP *http.Client
}

// CreatedRepo is a repository GitHub created.
type CreatedRepo struct {
	CloneURL string
	HTMLURL  string
	FullName string
}

// CreateRepo creates an empty repository named name, in the organisation owner or, when owner is empty, for the
// token's user. The token needs the repo scope (classic) or administration write (fine-grained).
func (g GitHub) CreateRepo(ctx context.Context, token, owner, name, description string, private bool) (CreatedRepo, error) {
	base := strings.TrimRight(g.BaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	path := "/user/repos"
	if owner != "" {
		path = "/orgs/" + url.PathEscape(owner) + "/repos"
	}
	body, err := json.Marshal(map[string]any{
		"name": name, "description": description, "private": private, "auto_init": false, "has_wiki": false,
	})
	if err != nil {
		return CreatedRepo{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return CreatedRepo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return CreatedRepo{}, fmt.Errorf("github: create repository: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		var e struct {
			Message string `json:"message"`
			Errors  []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Message
		for _, x := range e.Errors {
			if x.Message != "" {
				msg += "; " + x.Message
			}
		}
		return CreatedRepo{}, fmt.Errorf("github: create repository %s: %s (%d)", name, msg, resp.StatusCode)
	}
	var out struct {
		CloneURL string `json:"clone_url"`
		HTMLURL  string `json:"html_url"`
		FullName string `json:"full_name"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return CreatedRepo{}, fmt.Errorf("github: decode the created repository: %w", err)
	}
	if out.CloneURL == "" {
		return CreatedRepo{}, fmt.Errorf("github: the answer names no clone_url")
	}
	return CreatedRepo(out), nil
}
