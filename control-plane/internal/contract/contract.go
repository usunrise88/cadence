// Package contract loads api/openapi.yaml and api/vocabulary.yaml, enforces the naming rules of R1
// (docs/spec/08-resolutions.md) and derives the artefacts generated from them: the MCP tool manifest, the Go stubs
// for planned operations and the TypeScript operation table used by the web command registry.
package contract

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"
)

// Verb is one row of api/vocabulary.yaml.
type Verb struct {
	Name       string `yaml:"-" json:"name"`
	Class      string `yaml:"class" json:"class"`
	Shape      string `yaml:"shape" json:"shape"`
	Reversible *bool  `yaml:"reversible" json:"reversible"`
	Confirm    string `yaml:"confirm" json:"confirm"`
	Icon       string `yaml:"icon" json:"icon"`
	Key        string `yaml:"key,omitempty" json:"key,omitempty"`
	AgentOnly  bool   `yaml:"agentOnly,omitempty" json:"agentOnly,omitempty"`
}

// Vocabulary is the parsed api/vocabulary.yaml.
type Vocabulary struct {
	Verbs      map[string]Verb `yaml:"verbs"`
	ExemptTags []string        `yaml:"exemptTags"`
}

// Operation is one operation of the contract with its derived names.
type Operation struct {
	ID         string // <entity>.<verb>
	Entity     string // lowerCamel plural, as in the operationId
	Verb       string
	Kind       string // snake singular: EntityKind and topic segment
	Segment    string // kebab plural: path segment
	Method     string
	Path       string
	Summary    string
	ToolDesc   string
	Tags       []string
	Planned    int // phase that implements it; 0 = implemented
	Exempt     bool
	Mutation   bool
	op         *openapi3.Operation
	pathItem   *openapi3.PathItem
	pathEntity pathExt
}

type pathExt struct {
	Entity    string `json:"entity"`
	Kind      string `json:"kind"`
	Planned   int    `json:"planned"`
	Singleton bool   `json:"singleton"`
}

type opExt struct {
	ToolDescription string `json:"toolDescription"`
}

// Contract is the loaded contract.
type Contract struct {
	Doc   *openapi3.T
	Vocab Vocabulary
	Ops   []*Operation
}

// Load reads the OpenAPI document and the vocabulary. It does not validate naming; call Check for that.
func Load(specPath, vocabPath string) (*Contract, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", specPath, err)
	}
	raw, err := os.ReadFile(vocabPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", vocabPath, err)
	}
	var vocab Vocabulary
	if err := yaml.Unmarshal(raw, &vocab); err != nil {
		return nil, fmt.Errorf("parse %s: %w", vocabPath, err)
	}
	for name, v := range vocab.Verbs {
		v.Name = name
		vocab.Verbs[name] = v
	}
	c := &Contract{Doc: doc, Vocab: vocab}
	for path, item := range doc.Paths.Map() {
		var pe pathExt
		if err := ext(item.Extensions, &pe); err != nil {
			return nil, fmt.Errorf("%s: x-cadence: %w", path, err)
		}
		for method, op := range item.Operations() {
			var oe opExt
			if err := ext(op.Extensions, &oe); err != nil {
				return nil, fmt.Errorf("%s %s: x-cadence: %w", method, path, err)
			}
			o := &Operation{
				ID: op.OperationID, Method: method, Path: path, Summary: op.Summary, ToolDesc: oe.ToolDescription,
				Tags: op.Tags, Planned: pe.Planned, op: op, pathItem: item, pathEntity: pe,
				Mutation: method != "GET",
			}
			if ent, verb, ok := strings.Cut(op.OperationID, "."); ok {
				o.Entity, o.Verb = ent, verb
			}
			o.Segment = Kebab(pe.Entity)
			o.Kind = pe.Kind
			if o.Kind == "" {
				o.Kind = Snake(Singular(pe.Entity))
			}
			for _, t := range op.Tags {
				for _, ex := range vocab.ExemptTags {
					if t == ex {
						o.Exempt = true
					}
				}
			}
			c.Ops = append(c.Ops, o)
		}
	}
	sort.Slice(c.Ops, func(i, j int) bool { return c.Ops[i].ID < c.Ops[j].ID })
	return c, nil
}

func ext(m map[string]any, into any) error {
	v, ok := m["x-cadence"]
	if !ok {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

var (
	opIDRe   = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*\.[a-z]+$`)
	entityRe = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)
	paramRe  = regexp.MustCompile(`^\{[a-zA-Z]+\}$`)
)

// shape classifies a path and method into the vocabulary's shape names (R1).
func shape(o *Operation) (sh, actionVerb, segment string) {
	path := o.Path
	if i := strings.LastIndex(path, ":"); i > strings.LastIndex(path, "/") {
		actionVerb = path[i+1:]
		path = path[:i]
	}
	segs := strings.Split(strings.Trim(path, "/"), "/")
	last := segs[len(segs)-1]
	isItem := paramRe.MatchString(last)
	if isItem && len(segs) > 1 {
		segment = segs[len(segs)-2]
	} else {
		segment = last
	}
	if o.pathEntity.Singleton && !isItem {
		isItem = true
	}
	switch {
	case actionVerb != "":
		return "action", actionVerb, segment
	case o.Method == "GET" && isItem:
		return "item-get", "", segment
	case o.Method == "GET":
		for _, p := range o.params() {
			if p.In == "query" && p.Name == "q" {
				return "search", "", segment
			}
		}
		return "collection-get", "", segment
	case o.Method == "POST" && !isItem:
		return "collection-post", "", segment
	case o.Method == "PATCH" && isItem:
		return "item-patch", "", segment
	case o.Method == "PUT" && isItem:
		return "pointer-put", "", segment
	}
	return "unsupported:" + o.Method, "", segment
}

func (o *Operation) params() []*openapi3.Parameter {
	var out []*openapi3.Parameter
	for _, ps := range []openapi3.Parameters{o.pathItem.Parameters, o.op.Parameters} {
		for _, p := range ps {
			if p.Value != nil {
				out = append(out, p.Value)
			}
		}
	}
	return out
}

func (o *Operation) paramRef(in, name string) (ref string, found bool) {
	for _, ps := range []openapi3.Parameters{o.pathItem.Parameters, o.op.Parameters} {
		for _, p := range ps {
			if p.Value != nil && p.Value.In == in && strings.EqualFold(p.Value.Name, name) {
				return p.Ref, true
			}
		}
	}
	return "", false
}

// Check enforces the naming contract. It returns every violation, not just the first.
func (c *Contract) Check() []error {
	var errs []error
	fail := func(o *Operation, format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s %s (%s): %s", o.Method, o.Path, o.ID, fmt.Sprintf(format, a...)))
	}
	seen := map[string]bool{}
	for _, o := range c.Ops {
		if !opIDRe.MatchString(o.ID) {
			fail(o, "operationId must be <entity>.<verb> in lowerCamel")
			continue
		}
		if seen[o.ID] {
			fail(o, "duplicate operationId")
		}
		seen[o.ID] = true
		if o.pathEntity.Entity == "" {
			fail(o, "path has no x-cadence.entity")
			continue
		}
		if !entityRe.MatchString(o.pathEntity.Entity) {
			fail(o, "x-cadence.entity %q must be lowerCamel", o.pathEntity.Entity)
		}
		if o.Entity != o.pathEntity.Entity {
			fail(o, "entity %q differs from the path's x-cadence.entity %q", o.Entity, o.pathEntity.Entity)
		}
		if strings.TrimSpace(o.Summary) == "" || strings.Contains(o.Summary, "\n") {
			fail(o, "needs a one-line summary")
		}
		if o.Method == "DELETE" {
			fail(o, "DELETE is not allowed; removal is archive (soft)")
		}
		verb, known := c.Vocab.Verbs[o.Verb]
		if !known && !o.Exempt {
			fail(o, "verb %q is not in api/vocabulary.yaml (add it to docs/spec/10-ui-shell.md first)", o.Verb)
		}
		sh, actionVerb, segment := shape(o)
		if segment != o.Segment && !(o.pathEntity.Singleton && segment == o.pathEntity.Entity) {
			fail(o, "path segment %q must be the kebab plural of the entity (%q)", segment, o.Segment)
		}
		if known {
			switch {
			case sh == "action":
				if actionVerb != o.Verb {
					fail(o, "action suffix :%s must equal the verb %q", actionVerb, o.Verb)
				}
				wantMethod := "POST"
				if verb.Class == "read" {
					wantMethod = "GET"
				}
				if o.Method != wantMethod {
					fail(o, "action with a %s verb must be %s", verb.Class, wantMethod)
				}
			case verb.Shape != sh:
				fail(o, "HTTP shape %s requires a different verb than %q (whose shape is %s)", sh, o.Verb, verb.Shape)
			}
		}
		c.checkCommand(o, sh, fail)
		if len(o.Tags) == 0 {
			fail(o, "needs a tag")
		}
		if (o.Planned > 0) != hasTag(o, "planned") {
			fail(o, "x-cadence.planned and the planned tag must agree")
		}
		if d := o.op.Responses.Default(); d == nil || d.Ref != "#/components/responses/Problem" {
			fail(o, "default response must be #/components/responses/Problem")
		}
		if r := o.op.Responses.Value("202"); r != nil {
			if r.Ref != "#/components/responses/JobAccepted" && r.Ref != "#/components/responses/ApprovalAccepted" {
				fail(o, "202 must be JobAccepted or ApprovalAccepted")
			}
		}
	}
	return errs
}

func (c *Contract) checkCommand(o *Operation, sh string, fail func(*Operation, string, ...any)) {
	const comp = "#/components/parameters/"
	if hasTag(o, "auth") && o.Mutation {
		// Sign-in operations are not commands: there is no actor before sign-in to key idempotency on, and a
		// browser session is not an entity with a revision (docs/spec/06-platform.md "Authentication and access").
		return
	}
	if (hasTag(o, "host") || hasTag(o, "worker")) && o.Exempt {
		// The agent host's and the worker's protocols are not commands either: a report upserts entries under keys
		// the caller chose, so a retry is idempotent by construction, and nothing they post is dry-run or based on a
		// revision.
		return
	}
	if hasTag(o, "media") && o.Exempt && o.pathEntity.Singleton {
		// An utterance's media (audio.sign) mints a signed link: it changes no entity and has no revision to match
		// or effect to dry-run; the audit row it writes is its record. Media collections (transcriptions.new) still
		// follow the command rules below.
		return
	}
	ref, hasKey := o.paramRef("header", "Idempotency-Key")
	_, hasDry := o.paramRef("query", "dryRun")
	ifRef, hasIf := o.paramRef("header", "If-Match")
	if !o.Mutation {
		if hasKey || hasDry || hasIf {
			fail(o, "read operations take no Idempotency-Key, If-Match or dryRun")
		}
		return
	}
	if !hasKey || ref != comp+"IdempotencyKey" {
		fail(o, "mutation needs the shared IdempotencyKey parameter")
	}
	if ref, ok := o.paramRef("query", "dryRun"); !ok || ref != comp+"DryRun" {
		fail(o, "mutation needs the shared DryRun parameter")
	}
	itemMutation := sh == "item-patch" || sh == "pointer-put" || (sh == "action" && itemPath(o.Path))
	if itemMutation && (!hasIf || (ifRef != comp+"IfMatch" && ifRef != comp+"IfMatchOptional")) {
		fail(o, "mutation of an existing item needs the shared IfMatch (or IfMatchOptional for pointers) parameter")
	}
	if sh == "pointer-put" || !itemMutation {
		return
	}
	if ifRef == comp+"IfMatchOptional" {
		fail(o, "only pointers (PUT) may use IfMatchOptional")
	}
}

func itemPath(p string) bool {
	if i := strings.LastIndex(p, ":"); i > strings.LastIndex(p, "/") {
		p = p[:i]
	}
	segs := strings.Split(p, "/")
	return paramRe.MatchString(segs[len(segs)-1])
}

func hasTag(o *Operation, tag string) bool {
	for _, t := range o.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Kebab turns lowerCamel into kebab-case: goldenSets → golden-sets.
func Kebab(s string) string { return caseSplit(s, '-') }

// Snake turns lowerCamel into snake_case: goldenSet → golden_set.
func Snake(s string) string { return caseSplit(s, '_') }

func caseSplit(s string, sep byte) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch >= 'A' && ch <= 'Z' {
			if i > 0 {
				b.WriteByte(sep)
			}
			ch += 'a' - 'A'
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// Singular derives the singular of an English plural entity name; x-cadence.kind overrides it where the rule fails.
func Singular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies"):
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "sses"), strings.HasSuffix(s, "xes"), strings.HasSuffix(s, "ches"), strings.HasSuffix(s, "shes"):
		return s[:len(s)-2]
	case strings.HasSuffix(s, "ss"):
		return s
	case strings.HasSuffix(s, "s"):
		return s[:len(s)-1]
	}
	return s
}
