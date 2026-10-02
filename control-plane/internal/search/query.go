package search

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// The query language (docs/spec/11-ui-panels.md "Query language"): free text plus qualifiers. A token is
//
//	word | "a phrase"                        free text
//	field:value | field:"a value"            a qualifier; kind:a,b lists alternatives
//	field:>v  field:>=v  field:<v  field:<=v comparisons (updated and numeric fields)
//	field:a..b                               a range, both ends included
//	numeric<v  numeric<=v  numeric>v  numeric>=v  numeric=v   shorthand for numeric fields (wer<10)
//
// A token that looks like a qualifier (a letter-led name followed by ':' or a comparison) must name a known
// field; anything else fails with invalid-query listing the qualifiers, so a typo never silently searches text.

// Operators of a qualifier.
const (
	OpIs    = ":"
	OpLT    = "<"
	OpLE    = "<="
	OpGT    = ">"
	OpGE    = ">="
	OpEq    = "="
	OpRange = ".."
)

// Qualifier is one parsed qualifier; its JSON form is the contract's SearchQualifier.
type Qualifier struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
	Raw   string `json:"raw"`
}

// Term is one piece of free text: a word or a quoted phrase, normalised.
type Term struct {
	Text   string
	Phrase bool
}

// TimeBound is one condition on the updated time: At ≤ updated < Before (either may be zero).
type TimeBound struct {
	From, Before time.Time
}

// NumBound is one condition on a numeric field.
type NumBound struct {
	Field  string
	Op     string // <, <=, >, >=, =, ..
	Value  float64
	Value2 float64 // the upper end of a range
}

// Query is a parsed query.
type Query struct {
	Raw        string
	Text       string // the free text as typed, without the qualifiers
	Terms      []Term
	Qualifiers []Qualifier

	Kinds    []string
	Statuses []string
	Langs    []string
	Actors   []string // agent | user | automation, or an actor id or name
	Updated  []TimeBound
	Projects []string // slugs
	Scope    string   // "", all, project, registry
	Tags     []string
	Aliases  []string
	Numbers  []NumBound

	free []Term // the free text as typed (Text unnormalised), for Refold
}

// Refold normalises the free text again with fold (FoldFor: the locale's scoring normalizer, then Normalize), so
// terms match documents the index folded the same way.
func (out *Query) Refold(fold func(string) string) {
	out.Terms = nil
	for _, t := range out.free {
		if t.Phrase {
			if n := strings.TrimSpace(fold(t.Text)); n != "" {
				out.Terms = append(out.Terms, Term{Text: n, Phrase: true})
			}
			continue
		}
		for _, w := range Words(fold(t.Text)) {
			out.Terms = append(out.Terms, Term{Text: w})
		}
	}
}

// Scope values of the scope: qualifier.
const (
	ScopeAll      = "all"
	ScopeProject  = "project"
	ScopeRegistry = "registry"
)

type fieldKind int

const (
	fieldText fieldKind = iota
	fieldTime
	fieldNumber
)

// fields is the qualifier table: name → what it filters. Synonyms map to their canonical name.
var fields = map[string]fieldKind{
	"kind": fieldText, "status": fieldText, "lang": fieldText, "actor": fieldText, "updated": fieldTime,
	"project": fieldText, "scope": fieldText, "tag": fieldText, "alias": fieldText,
	"wer": fieldNumber, "cer": fieldNumber, "dur": fieldNumber, "hours": fieldNumber, "progress": fieldNumber,
	"rev": fieldNumber,
}

var synonyms = map[string]string{"state": "status", "locale": "lang", "is": "kind"}

// NumericFields are the fields numeric comparisons read (a document's numbers).
func NumericFields() []string {
	var out []string
	for f, k := range fields {
		if k == fieldNumber {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out
}

// QualifierNames lists every qualifier with its synonyms, for error messages and help.
func QualifierNames() []string {
	var out []string
	for f, k := range fields {
		if k != fieldNumber {
			out = append(out, f+":")
		}
	}
	for s := range synonyms {
		out = append(out, s+":")
	}
	slices.Sort(out)
	return out
}

func qualifierList() string {
	return strings.Join(QualifierNames(), " ") + "; numeric: " + strings.Join(NumericFields(), " ") +
		" (wer<10, progress>=0.5, dur:2..8)"
}

// invalid is the invalid-query problem with the qualifier list appended.
func invalid(format string, args ...any) error {
	return problems.InvalidQuery.New("%s. Qualifiers: %s", fmt.Sprintf(format, args...), qualifierList())
}

var (
	qualifierRe  = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*):(.*)$`)
	comparisonRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*)(<=|>=|<|>|=)(.*)$`)
	langRe       = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
	slugRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	dayLayout    = "2006-01-02"
)

// Parse parses q. kinds maps every accepted kind: value (and its aliases) to the kind; nil accepts none.
func Parse(q string, kinds map[string]string) (Query, error) {
	out := Query{Raw: q}
	tokens, err := tokenize(q)
	if err != nil {
		return Query{}, err
	}
	var text []string
	for _, tok := range tokens {
		if tok.quoted {
			text = append(text, `"`+tok.text+`"`)
			out.free = append(out.free, Term{Text: tok.text, Phrase: true})
			continue
		}
		qual, isQual, err := parseQualifier(tok)
		if err != nil {
			return Query{}, err
		}
		if !isQual {
			text = append(text, tok.text)
			out.free = append(out.free, Term{Text: tok.text})
			continue
		}
		if err := out.apply(qual, kinds); err != nil {
			return Query{}, err
		}
		out.Qualifiers = append(out.Qualifiers, qual)
	}
	out.Text = strings.Join(text, " ")
	out.Refold(Normalize)
	return out, nil
}

type token struct {
	text     string // without quotes
	raw      string
	quoted   bool   // the whole token is a quoted phrase
	valueRaw string // for field:"quoted value", the unquoted value
	hasValue bool
}

// tokenize splits on whitespace outside double quotes. field:"a b" keeps its quoted value together.
func tokenize(q string) ([]token, error) {
	var (
		out []token
		cur strings.Builder
		in  bool
	)
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		raw := cur.String()
		cur.Reset()
		t := token{text: raw, raw: raw}
		switch {
		case len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"':
			t.quoted, t.text = true, raw[1:len(raw)-1]
		case strings.Contains(raw, `"`):
			// field:"quoted value" — the quotes belong to the value.
			i := strings.IndexByte(raw, '"')
			if strings.HasSuffix(raw, `"`) && i < len(raw)-1 {
				t.text = raw[:i] + raw[i+1:len(raw)-1]
				t.valueRaw, t.hasValue = raw[i+1:len(raw)-1], true
			} else {
				t.text = strings.ReplaceAll(raw, `"`, "")
			}
		}
		out = append(out, t)
	}
	for _, r := range q {
		switch {
		case r == '"':
			in = !in
			cur.WriteRune(r)
		case !in && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	if in {
		return nil, invalid("a quote is not closed in %q", q)
	}
	flush()
	return out, nil
}

// parseQualifier recognises tok as a qualifier; isQual is false for free text.
func parseQualifier(tok token) (Qualifier, bool, error) {
	if m := qualifierRe.FindStringSubmatch(tok.text); m != nil {
		name := strings.ToLower(m[1])
		if s, ok := synonyms[name]; ok {
			name = s
		}
		kind, known := fields[name]
		if !known {
			return Qualifier{}, false, invalid("unknown qualifier %q", m[1]+":")
		}
		value := m[2]
		op := OpIs
		switch {
		case strings.HasPrefix(value, ">="), strings.HasPrefix(value, "<="):
			op, value = value[:2], value[2:]
		case strings.HasPrefix(value, ">"), strings.HasPrefix(value, "<"), strings.HasPrefix(value, "="):
			op, value = value[:1], value[1:]
			if op == OpEq {
				op = OpIs
			}
		case kind != fieldText && strings.Contains(value, ".."):
			op = OpRange
		}
		if op != OpIs && kind == fieldText {
			return Qualifier{}, false, invalid("%s: takes a value, not a comparison", name)
		}
		if strings.TrimSpace(value) == "" {
			return Qualifier{}, false, invalid("%s: needs a value", name)
		}
		if tok.hasValue && op == OpIs {
			value = tok.valueRaw
		}
		return Qualifier{Field: name, Op: op, Value: value, Raw: tok.raw}, true, nil
	}
	if m := comparisonRe.FindStringSubmatch(tok.text); m != nil {
		name := strings.ToLower(m[1])
		if fields[name] != fieldNumber {
			if _, known := fields[name]; known {
				return Qualifier{}, false, invalid("%s is not numeric; write %s:%s%s", name, name, m[2], m[3])
			}
			return Qualifier{}, false, invalid("unknown field %q in %q", m[1], tok.raw)
		}
		op := m[2]
		return Qualifier{Field: name, Op: op, Value: m[3], Raw: tok.raw}, true, nil
	}
	return Qualifier{}, false, nil
}

// apply validates q's value and records it on the query.
func (out *Query) apply(q Qualifier, kinds map[string]string) error {
	values := func() []string {
		var vs []string
		for _, v := range strings.Split(q.Value, ",") {
			if v = strings.TrimSpace(v); v != "" {
				vs = append(vs, v)
			}
		}
		return vs
	}
	switch fields[q.Field] {
	case fieldTime:
		b, err := timeBound(q)
		if err != nil {
			return err
		}
		out.Updated = append(out.Updated, b)
		return nil
	case fieldNumber:
		b, err := numBound(q)
		if err != nil {
			return err
		}
		out.Numbers = append(out.Numbers, b)
		return nil
	}
	switch q.Field {
	case "kind":
		for _, v := range values() {
			k, ok := kinds[strings.ToLower(v)]
			if !ok {
				return invalid("kind:%s is not a searchable kind; kinds: %s", v, strings.Join(kindNames(kinds), ", "))
			}
			if !slices.Contains(out.Kinds, k) {
				out.Kinds = append(out.Kinds, k)
			}
		}
	case "status":
		for _, v := range values() {
			out.Statuses = append(out.Statuses, strings.ToLower(v))
		}
	case "lang":
		for _, v := range values() {
			if !langRe.MatchString(v) {
				return invalid("lang:%s is not a locale (he, he-IL)", v)
			}
			out.Langs = append(out.Langs, strings.ToLower(v))
		}
	case "actor":
		out.Actors = append(out.Actors, values()...)
	case "project":
		for _, v := range values() {
			if !slugRe.MatchString(v) {
				return invalid("project:%s is not a project slug", v)
			}
			out.Projects = append(out.Projects, v)
		}
	case "scope":
		v := strings.ToLower(q.Value)
		if v != ScopeAll && v != ScopeProject && v != ScopeRegistry {
			return invalid("scope:%s — scope is all, project or registry", q.Value)
		}
		if out.Scope != "" && out.Scope != v {
			return invalid("scope:%s contradicts scope:%s", v, out.Scope)
		}
		out.Scope = v
	case "tag":
		for _, v := range values() {
			out.Tags = append(out.Tags, strings.ToLower(v))
		}
	case "alias":
		for _, v := range values() {
			out.Aliases = append(out.Aliases, strings.TrimPrefix(v, "@"))
		}
	}
	return nil
}

func kindNames(kinds map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range kinds {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// timeBound turns an updated: qualifier into a half-open interval. A day stands for the whole day (UTC):
// >d is after that day, <=d includes it; a..b includes both days.
func timeBound(q Qualifier) (TimeBound, error) {
	parse := func(s string) (t time.Time, day bool, err error) {
		if t, err := time.Parse(dayLayout, s); err == nil {
			return t, true, nil
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, false, nil
		}
		return time.Time{}, false, invalid("updated:%s — dates are YYYY-MM-DD or RFC 3339", s)
	}
	end := func(t time.Time, day bool) time.Time { // the first instant after t (or after its day)
		if day {
			return t.AddDate(0, 0, 1)
		}
		return t.Add(time.Nanosecond)
	}
	if q.Op == OpRange {
		a, b, _ := strings.Cut(q.Value, "..")
		from, _, err := parse(a)
		if err != nil {
			return TimeBound{}, err
		}
		to, day, err := parse(b)
		if err != nil {
			return TimeBound{}, err
		}
		if to.Before(from) {
			return TimeBound{}, invalid("updated:%s — the range ends before it starts", q.Value)
		}
		return TimeBound{From: from, Before: end(to, day)}, nil
	}
	t, day, err := parse(q.Value)
	if err != nil {
		return TimeBound{}, err
	}
	switch q.Op {
	case OpGT:
		return TimeBound{From: end(t, day)}, nil
	case OpGE:
		return TimeBound{From: t}, nil
	case OpLT:
		return TimeBound{Before: t}, nil
	case OpLE:
		return TimeBound{Before: end(t, day)}, nil
	}
	if day {
		return TimeBound{From: t, Before: end(t, true)}, nil
	}
	return TimeBound{From: t, Before: end(t, false)}, nil
}

func numBound(q Qualifier) (NumBound, error) {
	num := func(s string) (float64, error) {
		f, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if err != nil {
			return 0, invalid("%s: %q is not a number", q.Field, s)
		}
		return f, nil
	}
	if q.Op == OpRange {
		a, b, _ := strings.Cut(q.Value, "..")
		lo, err := num(a)
		if err != nil {
			return NumBound{}, err
		}
		hi, err := num(b)
		if err != nil {
			return NumBound{}, err
		}
		if hi < lo {
			return NumBound{}, invalid("%s:%s — the range ends before it starts", q.Field, q.Value)
		}
		return NumBound{Field: q.Field, Op: OpRange, Value: lo, Value2: hi}, nil
	}
	v, err := num(q.Value)
	if err != nil {
		return NumBound{}, err
	}
	op := q.Op
	if op == OpIs {
		op = OpEq
	}
	return NumBound{Field: q.Field, Op: op, Value: v}, nil
}
