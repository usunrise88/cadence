package events

import (
	"fmt"
	"strings"
)

// Pattern is one topic pattern: an exact topic, or a prefix followed by a trailing `*` segment that matches one
// or more remaining segments (`run.123.*` matches `run.123.metrics` and `run.123.a.b`, not `run.123`).
// A lone `*` matches every topic.
type Pattern struct {
	exact  string
	prefix string // with its trailing dot; set only for wildcard patterns
	all    bool
}

// ParsePattern parses one pattern; `*` is allowed only as the whole last segment.
func ParsePattern(s string) (Pattern, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "":
		return Pattern{}, fmt.Errorf("empty topic pattern")
	case "*":
		return Pattern{all: true}, nil
	}
	segments := strings.Split(s, ".")
	for i, seg := range segments {
		last := i == len(segments)-1
		if seg == "" {
			return Pattern{}, fmt.Errorf("topic pattern %q has an empty segment", s)
		}
		if strings.Contains(seg, "*") && (!last || seg != "*") {
			return Pattern{}, fmt.Errorf("topic pattern %q: `*` is allowed only as the whole last segment", s)
		}
	}
	if segments[len(segments)-1] == "*" {
		return Pattern{prefix: strings.TrimSuffix(s, "*")}, nil
	}
	return Pattern{exact: s}, nil
}

// Match reports whether topic matches p.
func (p Pattern) Match(topic string) bool {
	switch {
	case p.all:
		return true
	case p.prefix != "":
		return len(topic) > len(p.prefix) && strings.HasPrefix(topic, p.prefix)
	default:
		return topic == p.exact
	}
}

// Filter selects events by topic patterns and project.
type Filter struct {
	Patterns []Pattern // empty means every topic
	// ProjectID keeps events of this project plus events without a projectId (registry events); empty keeps all.
	ProjectID string
}

// ParseFilter builds a filter from a comma-separated pattern list and a project id.
func ParseFilter(topics, projectID string) (Filter, error) {
	f := Filter{ProjectID: projectID}
	if strings.TrimSpace(topics) == "" {
		return f, nil
	}
	for _, s := range strings.Split(topics, ",") {
		p, err := ParsePattern(s)
		if err != nil {
			return Filter{}, err
		}
		if p.all {
			f.Patterns = nil
			return f, nil
		}
		f.Patterns = append(f.Patterns, p)
	}
	return f, nil
}

// Match reports whether r passes f.
func (f Filter) Match(r Record) bool {
	if f.ProjectID != "" && r.ProjectID != "" && r.ProjectID != f.ProjectID {
		return false
	}
	if len(f.Patterns) == 0 {
		return true
	}
	for _, p := range f.Patterns {
		if p.Match(r.Topic) {
			return true
		}
	}
	return false
}

// sql renders f and the lower seq bound as a WHERE clause with positional arguments.
func (f Filter) sql(after int64) (string, []any) {
	args := []any{after}
	where := "seq > $1"
	if f.ProjectID != "" {
		args = append(args, f.ProjectID)
		where += fmt.Sprintf(" AND (project_id IS NULL OR project_id = $%d)", len(args))
	}
	if len(f.Patterns) > 0 {
		var exact, like []string
		for _, p := range f.Patterns {
			if p.prefix != "" {
				like = append(like, escapeLike(p.prefix)+"_%")
			} else {
				exact = append(exact, p.exact)
			}
		}
		var ors []string
		if len(exact) > 0 {
			args = append(args, exact)
			ors = append(ors, fmt.Sprintf("topic = ANY($%d)", len(args)))
		}
		if len(like) > 0 {
			args = append(args, like)
			ors = append(ors, fmt.Sprintf("topic LIKE ANY($%d)", len(args)))
		}
		where += " AND (" + strings.Join(ors, " OR ") + ")"
	}
	return where, args
}

// escapeLike escapes LIKE metacharacters with the default escape character (backslash).
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
