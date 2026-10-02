package langpacks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Boost is one boost list (boost/<domain>.txt).
//
// The file format, line by line:
//
//	# weight: 1.5            the list's weight (required, once, among the comment lines)
//	# any other comment      lines starting with # and blank lines are comments
//	Moshe Cohen              every other line is one term, trimmed; a term appears once
//
// A term is a whole phrase as the decoder should write it. The eval decoder receives the list as a boost_list
// artifact: the JSON of Artifact(), {"terms": [...], "weight": w}; SHA256 is the list's identity in an eval's
// decoding axis (R24).
type Boost struct {
	Domain string
	Weight float64
	Terms  []string
}

var weightRe = regexp.MustCompile(`^#\s*weight\s*:\s*(\S+)\s*$`)

// ParseBoost reads a boost list; maxTerms > 0 caps its length.
func ParseBoost(domain string, b []byte, maxTerms int) (Boost, error) {
	out := Boost{Domain: domain, Terms: []string{}}
	seen := map[string]int{}
	haveWeight := false
	for i, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#"):
			m := weightRe.FindStringSubmatch(strings.ToLower(line))
			if m == nil {
				continue
			}
			if haveWeight {
				return Boost{}, fmt.Errorf("line %d: a second weight header", i+1)
			}
			w, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				return Boost{}, fmt.Errorf("line %d: the weight %q is not a number", i+1, m[1])
			}
			out.Weight, haveWeight = w, true
		default:
			if err := checkTerm(line); err != nil {
				return Boost{}, fmt.Errorf("line %d: %w", i+1, err)
			}
			if first, dup := seen[line]; dup {
				return Boost{}, fmt.Errorf("line %d: %q is already on line %d", i+1, line, first)
			}
			seen[line] = i + 1
			out.Terms = append(out.Terms, line)
		}
	}
	if !haveWeight {
		return Boost{}, fmt.Errorf("a boost list starts with a '# weight: <number>' header")
	}
	if maxTerms > 0 && len(out.Terms) > maxTerms {
		return Boost{}, fmt.Errorf("%d terms; a list holds at most %d (defaults.yaml langpacks.boost_max_terms)", len(out.Terms), maxTerms)
	}
	return out, nil
}

func checkTerm(t string) error {
	switch {
	case strings.ContainsAny(t, "\n\r\t"):
		return fmt.Errorf("a term is one line without tabs")
	case strings.HasPrefix(t, "#"):
		return fmt.Errorf("a term cannot start with # (that is a comment)")
	case len([]rune(t)) > 200:
		return fmt.Errorf("a term is at most 200 characters")
	}
	return nil
}

// NormalizeTerms trims terms and checks them: no empty term, no duplicate, no comment marker.
func NormalizeTerms(terms []string) ([]string, error) {
	out := make([]string, 0, len(terms))
	seen := map[string]bool{}
	for i, t := range terms {
		t = strings.TrimSpace(t)
		if t == "" {
			return nil, fmt.Errorf("terms[%d] is empty", i)
		}
		if err := checkTerm(t); err != nil {
			return nil, fmt.Errorf("terms[%d]: %w", i, err)
		}
		if seen[t] {
			return nil, fmt.Errorf("terms[%d]: %q appears twice", i, t)
		}
		seen[t] = true
		out = append(out, t)
	}
	return out, nil
}

// Render writes the list in the file format. The comment lines of prev (the list's current file, or nil) other
// than its weight header are kept above the terms.
func (b Boost) Render(prev []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("# weight: " + strconv.FormatFloat(b.Weight, 'f', -1, 64) + "\n")
	for _, raw := range strings.Split(string(prev), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if strings.HasPrefix(line, "#") && weightRe.FindStringSubmatch(strings.ToLower(line)) == nil {
			buf.WriteString(line + "\n")
		}
	}
	for _, t := range b.Terms {
		buf.WriteString(t + "\n")
	}
	return buf.Bytes()
}

// artifact is the boost_list artifact's JSON.
type artifact struct {
	Terms  []string `json:"terms"`
	Weight float64  `json:"weight"`
}

// Artifact returns the boost_list artifact's content: {"terms": [...], "weight": w}, keys in that order, no
// insignificant whitespace.
func (b Boost) Artifact() []byte {
	terms := b.Terms
	if terms == nil {
		terms = []string{}
	}
	out, _ := json.Marshal(artifact{Terms: terms, Weight: b.Weight}) // strings and a checked float: cannot fail
	return out
}

// SHA256 is the hex sha256 of Artifact(): the list's identity.
func (b Boost) SHA256() string {
	sum := sha256.Sum256(b.Artifact())
	return hex.EncodeToString(sum[:])
}
