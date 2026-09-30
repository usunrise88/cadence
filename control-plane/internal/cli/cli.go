// Package cli is the generated command line of the API (R34, docs/spec/08-resolutions.md): `cadence <entity> <verb>
// [flags]` for every implemented operation outside the exempt tags (auth, me), talking to a control plane over
// HTTP with an API key. The command table (operations.gen.go) is generated from api/openapi.yaml by make gen; this
// file is the runtime that turns flags into one request and prints the answer as JSON.
package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Operation is one generated command.
type Operation struct {
	ID, Entity, Verb, Method, Path string
	Summary                        string
	Description                    string // the curated tool description, when the contract has one
	IdempotencyKey                 bool   // a command: it takes an Idempotency-Key (generated per call)
	Params                         []Param
	Body                           *Body
}

// Param is one path, query or If-Match parameter and its flag.
type Param struct {
	Name, In, Flag string
	Required       bool
	Type           string // string, integer, number, boolean, array
	Items          string // item type of an array
	Description    string
	Default        string
	Enum           []string
}

// Body describes an operation's JSON request body, for help and the required check.
type Body struct {
	Required   bool
	Properties []BodyProperty
}

// BodyProperty is one top-level property of a request body.
type BodyProperty struct {
	Name        string
	Required    bool
	Type        string
	Description string
}

// Environment variables the CLI reads.
const (
	EnvURL   = "CADENCE_URL"
	EnvToken = "CADENCE_TOKEN" //nolint:gosec // the name of the variable, not a credential
	// DefaultURL is the control plane's default listen address (cadence serve).
	DefaultURL = "http://127.0.0.1:8080"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1 // the request failed or the server answered an error
	ExitUsage = 2 // the command line is wrong
)

// Options are the runtime's injectable dependencies; zero values use the real ones.
type Options struct {
	HTTPClient *http.Client
	// NewKey mints an Idempotency-Key for a command; default a random UUIDv4.
	NewKey func() string
}

// Known reports whether name is an entity with generated commands.
func Known(name string) bool {
	for _, o := range Operations {
		if o.Entity == name {
			return true
		}
	}
	return false
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// Run executes one command line (without the program name): `help [entity [verb]]` or `<entity> <verb> [flags]`.
// It returns the process exit code.
func Run(ctx context.Context, args []string, env func(string) string, stdin io.Reader, stdout, stderr io.Writer, opts Options) int {
	// The help entity shares its name with this help command: `cadence help get --id …` and `cadence help search`
	// call help.get and help.search (their verbs are not entity names); `cadence help <entity>` describes an entity.
	helpVerb := len(args) > 1 && args[0] == "help" && !Known(args[1])
	if helpVerb {
		_, helpVerb = find("help", args[1])
	}
	if !helpVerb && (len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		var rest []string
		if len(args) > 0 {
			rest = args[1:]
		}
		return help(rest, stdout, stderr)
	}
	entity := args[0]
	if !Known(entity) {
		_, _ = fmt.Fprintf(stderr, "cadence: unknown entity %q; `cadence help` lists them\n", entity)
		return ExitUsage
	}
	if len(args) < 2 || strings.HasPrefix(args[1], "-") {
		writeEntityHelp(stderr, entity)
		return ExitUsage
	}
	op, ok := find(entity, args[1])
	if !ok {
		_, _ = fmt.Fprintf(stderr, "cadence: %s has no verb %q\n\n", entity, args[1])
		writeEntityHelp(stderr, entity)
		return ExitUsage
	}
	inv, err := parse(op, args[2:])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cadence %s %s: %v\n\n", op.Entity, op.Verb, err)
		writeOpHelp(stderr, op)
		return ExitUsage
	}
	if inv.help {
		writeOpHelp(stdout, op)
		return ExitOK
	}
	code, err := execute(ctx, op, inv, env, stdin, stdout, stderr, opts)
	if err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			_, _ = fmt.Fprintf(stderr, "cadence %s %s: %v\n", op.Entity, op.Verb, err)
			return ExitUsage
		}
		_, _ = fmt.Fprintf(stderr, "cadence %s %s: %v\n", op.Entity, op.Verb, err)
		return ExitError
	}
	return code
}

func find(entity, verb string) (Operation, bool) {
	for _, o := range Operations {
		if o.Entity == entity && o.Verb == verb {
			return o, true
		}
	}
	return Operation{}, false
}

// invocation is a parsed command line.
type invocation struct {
	values   map[string][]string // flag → values, for the operation's parameters
	url      string
	token    string
	body     string // as given: JSON, @file or @-
	hasBody  bool
	key      string
	help     bool
	hasURL   bool
	hasToken bool
}

func parse(op Operation, args []string) (invocation, error) {
	inv := invocation{values: map[string][]string{}}
	byFlag := map[string]Param{}
	for _, p := range op.Params {
		byFlag[p.Flag] = p
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-h" || a == "--help" {
			inv.help = true
			continue
		}
		if !strings.HasPrefix(a, "--") || len(a) == 2 {
			return inv, usagef("unexpected argument %q; every value is passed as --flag value", a)
		}
		name, value, hasValue := strings.Cut(a[2:], "=")
		p, isParam := byFlag[name]
		boolean := isParam && p.Type == "boolean"
		if !hasValue && !boolean {
			if i+1 >= len(args) {
				return inv, usagef("--%s needs a value", name)
			}
			i++
			value, hasValue = args[i], true
		}
		switch {
		case isParam:
			if boolean && !hasValue {
				value = "true"
			}
			inv.values[name] = append(inv.values[name], value)
		case name == "url":
			inv.url, inv.hasURL = value, true
		case name == "token":
			inv.token, inv.hasToken = value, true
		case name == "body":
			if op.Body == nil {
				return inv, usagef("%s takes no body", op.ID)
			}
			inv.body, inv.hasBody = value, true
		case name == "idempotency-key":
			if !op.IdempotencyKey {
				return inv, usagef("%s is a read; it takes no idempotency key", op.ID)
			}
			inv.key = value
		default:
			return inv, usagef("unknown flag --%s", name)
		}
	}
	if inv.help {
		return inv, nil
	}
	var missing []string
	for _, p := range op.Params {
		if p.Required && len(inv.values[p.Flag]) == 0 {
			missing = append(missing, "--"+p.Flag)
		}
	}
	if op.Body != nil && op.Body.Required && !inv.hasBody {
		missing = append(missing, "--body")
	}
	if len(missing) > 0 {
		return inv, usagef("missing %s", strings.Join(missing, ", "))
	}
	for _, p := range op.Params {
		for _, v := range inv.values[p.Flag] {
			if err := checkValue(p, v); err != nil {
				return inv, err
			}
		}
		if len(inv.values[p.Flag]) > 1 && p.Type != "array" {
			return inv, usagef("--%s given more than once", p.Flag)
		}
	}
	return inv, nil
}

func checkValue(p Param, v string) error {
	typ := p.Type
	if typ == "array" {
		typ = p.Items
	}
	switch typ {
	case "boolean":
		if _, err := strconv.ParseBool(v); err != nil {
			return usagef("--%s takes true or false, not %q", p.Flag, v)
		}
	case "integer":
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			return usagef("--%s takes an integer, not %q", p.Flag, v)
		}
	case "number":
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return usagef("--%s takes a number, not %q", p.Flag, v)
		}
	}
	if len(p.Enum) > 0 && p.Type != "array" {
		for _, e := range p.Enum {
			if e == v {
				return nil
			}
		}
		return usagef("--%s must be one of %s, not %q", p.Flag, strings.Join(p.Enum, ", "), v)
	}
	return nil
}

// BaseURL turns a server URL (CADENCE_URL) into the API base: /api is appended unless the URL already ends with it.
func BaseURL(raw string) string {
	u := strings.TrimRight(strings.TrimSpace(raw), "/")
	if u == "" {
		u = DefaultURL
	}
	if !strings.HasSuffix(u, "/api") {
		u += "/api"
	}
	return u
}

func execute(ctx context.Context, op Operation, inv invocation, env func(string) string, stdin io.Reader, stdout, stderr io.Writer, opts Options) (int, error) {
	base := env(EnvURL)
	if inv.hasURL {
		base = inv.url
	}
	token := env(EnvToken)
	if inv.hasToken {
		token = inv.token
	}
	var body []byte
	if inv.hasBody {
		var err error
		if body, err = readBody(inv.body, stdin); err != nil {
			return 0, err
		}
	}

	path := op.Path
	query := url.Values{}
	header := http.Header{}
	for _, p := range op.Params {
		vals := inv.values[p.Flag]
		if len(vals) == 0 {
			continue
		}
		switch p.In {
		case "path":
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(vals[0]))
		case "query":
			for _, v := range vals {
				if p.Type == "array" {
					for _, item := range strings.Split(v, ",") {
						query.Add(p.Name, strings.TrimSpace(item))
					}
					continue
				}
				query.Add(p.Name, v)
			}
		case "header":
			v := vals[0]
			if strings.EqualFold(p.Name, "If-Match") {
				v = ifMatch(v)
			}
			header.Set(p.Name, v)
		}
	}
	target := BaseURL(base) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, op.Method, target, reader)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header = header
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cadence-cli")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if op.IdempotencyKey {
		key := inv.key
		if key == "" {
			newKey := opts.NewKey
			if newKey == nil {
				newKey = randomKey
			}
			key = newKey()
		}
		req.Header.Set("Idempotency-Key", key)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err // a *url.Error already names the method and URL
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read the response: %w", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		writeJSON(stdout, respBody)
		return ExitOK, nil
	}
	_, _ = fmt.Fprintln(stderr, problemLine(resp.StatusCode, respBody))
	writeJSON(stderr, respBody)
	return ExitError, nil
}

// readBody reads --body: inline JSON, @file or @- (standard input); it must be valid JSON.
func readBody(v string, stdin io.Reader) ([]byte, error) {
	var b []byte
	switch {
	case v == "@-":
		var err error
		if b, err = io.ReadAll(stdin); err != nil {
			return nil, fmt.Errorf("read the body from standard input: %w", err)
		}
	case strings.HasPrefix(v, "@"):
		var err error
		if b, err = os.ReadFile(v[1:]); err != nil {
			return nil, usagef("read --body file: %v", err)
		}
	default:
		b = []byte(v)
	}
	b = bytes.TrimSpace(b)
	if !json.Valid(b) {
		return nil, usagef("--body is not valid JSON")
	}
	return b, nil
}

// ifMatch accepts 3, "3" or W/"3" and sends the quoted form the server's ETags use.
func ifMatch(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "W/") {
		return v
	}
	return `"` + v + `"`
}

func writeJSON(w io.Writer, b []byte) {
	if len(bytes.TrimSpace(b)) == 0 {
		return
	}
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		_, _ = w.Write(b)
		if !bytes.HasSuffix(b, []byte("\n")) {
			_, _ = io.WriteString(w, "\n")
		}
		return
	}
	out.WriteByte('\n')
	_, _ = w.Write(out.Bytes())
}

// problemLine summarises an error answer: status, title, detail and the help article of its type.
func problemLine(status int, body []byte) string {
	var p struct {
		Type, Title, Detail string
	}
	line := fmt.Sprintf("cadence: HTTP %d", status)
	if err := json.Unmarshal(body, &p); err != nil || p.Title == "" {
		return line
	}
	line += " " + p.Title
	if p.Detail != "" {
		line += ": " + p.Detail
	}
	if slug, ok := strings.CutPrefix(p.Type, "https://cadence.local/help/errors/"); ok {
		line += " (help: errors." + slug + ")"
	}
	return line
}

func randomKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---------------------------------------------------------------- help

const globalHelp = `Global flags:
  --url URL               control plane (env CADENCE_URL, default http://127.0.0.1:8080; /api is appended)
  --token TOKEN           API key, sent as a Bearer token (env CADENCE_TOKEN; create one with credentials new)
  --idempotency-key KEY   commands only: reuse a key to replay a command's first result (default: a new key)
  -h, --help              this command's flags`

func help(args []string, stdout, stderr io.Writer) int {
	switch len(args) {
	case 0:
		writeOverview(stdout)
		return ExitOK
	case 1:
		if !Known(args[0]) {
			_, _ = fmt.Fprintf(stderr, "cadence help: unknown entity %q\n\n", args[0])
			writeOverview(stderr)
			return ExitUsage
		}
		writeEntityHelp(stdout, args[0])
		return ExitOK
	default:
		op, ok := find(args[0], args[1])
		if !ok {
			_, _ = fmt.Fprintf(stderr, "cadence help: no command %s %s\n", args[0], args[1])
			return ExitUsage
		}
		writeOpHelp(stdout, op)
		return ExitOK
	}
}

func entities() map[string][]Operation {
	m := map[string][]Operation{}
	for _, o := range Operations {
		m[o.Entity] = append(m[o.Entity], o)
	}
	return m
}

func writeOverview(w io.Writer) {
	_, _ = fmt.Fprintln(w, `usage: cadence <entity> <verb> [flags]
       cadence help [<entity> [<verb>]]
       cadence serve | admin | version

Every command is one API operation (<entity>.<verb>, the same name as the MCP tool and the UI command); the answer
is printed as JSON. Errors print the problem to standard error and exit 1; a wrong command line exits 2.
The help entity's own verbs read "cadence help get --id <article>" and "cadence help search --q <text>".

Entities:`)
	m := entities()
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		verbs := make([]string, 0, len(m[n]))
		for _, o := range m[n] {
			verbs = append(verbs, o.Verb)
		}
		_, _ = fmt.Fprintf(w, "  %-14s %s\n", n, strings.Join(verbs, " "))
	}
	_, _ = fmt.Fprintln(w, "\n"+globalHelp)
}

func writeEntityHelp(w io.Writer, entity string) {
	_, _ = fmt.Fprintf(w, "usage: cadence %s <verb> [flags]\n\nVerbs:\n", entity)
	for _, o := range entities()[entity] {
		_, _ = fmt.Fprintf(w, "  %-10s %s\n", o.Verb, o.Summary)
		if flags := flagSynopsis(o); flags != "" {
			_, _ = fmt.Fprintf(w, "  %-10s %s\n", "", flags)
		}
	}
	_, _ = fmt.Fprintf(w, "\n`cadence %s <verb> --help` describes one verb's flags and body.\n", entity)
}

func flagSynopsis(o Operation) string {
	var parts []string
	for _, p := range o.Params {
		s := "--" + p.Flag
		if p.Type != "boolean" {
			s += " " + placeholder(p)
		}
		if !p.Required {
			s = "[" + s + "]"
		}
		parts = append(parts, s)
	}
	if o.Body != nil {
		if o.Body.Required {
			parts = append(parts, "--body JSON")
		} else {
			parts = append(parts, "[--body JSON]")
		}
	}
	return strings.Join(parts, " ")
}

func placeholder(p Param) string {
	switch p.Type {
	case "integer", "number":
		return "N"
	case "array":
		return "A,B"
	}
	return strings.ToUpper(strings.ReplaceAll(p.Flag, "-", "_"))
}

func writeOpHelp(w io.Writer, o Operation) {
	_, _ = fmt.Fprintf(w, "usage: cadence %s %s %s\n\n%s (%s %s, operation %s)\n", o.Entity, o.Verb, flagSynopsis(o), o.Summary, o.Method, o.Path, o.ID)
	if o.Description != "" {
		_, _ = fmt.Fprintf(w, "\n%s\n", wrap(o.Description, 100))
	}
	if len(o.Params) > 0 {
		_, _ = fmt.Fprintln(w, "\nFlags:")
		for _, p := range o.Params {
			var facts []string
			if p.Required {
				facts = append(facts, "required")
			}
			if p.Type != "" && p.Type != "string" {
				t := p.Type
				if p.Items != "" {
					t += " of " + p.Items + " (comma-separated or repeated)"
				}
				facts = append(facts, t)
			}
			if p.Default != "" {
				facts = append(facts, "default "+p.Default)
			}
			if len(p.Enum) > 0 {
				facts = append(facts, "one of "+strings.Join(p.Enum, ", "))
			}
			line := fmt.Sprintf("  --%-18s", p.Flag)
			if p.Description != "" {
				line += " " + p.Description
			}
			if len(facts) > 0 {
				line += " [" + strings.Join(facts, "; ") + "]"
			}
			_, _ = fmt.Fprintln(w, line)
		}
	}
	if o.Body != nil {
		req := "optional"
		if o.Body.Required {
			req = "required"
		}
		_, _ = fmt.Fprintf(w, "\nBody (--body JSON, --body @file or --body @- for standard input; %s):\n", req)
		for _, p := range o.Body.Properties {
			line := fmt.Sprintf("  %-20s", p.Name)
			var facts []string
			if p.Required {
				facts = append(facts, "required")
			}
			if p.Type != "" {
				facts = append(facts, p.Type)
			}
			if len(facts) > 0 {
				line += " (" + strings.Join(facts, ", ") + ")"
			}
			if p.Description != "" {
				line += " " + p.Description
			}
			_, _ = fmt.Fprintln(w, strings.TrimRight(line, " "))
		}
	}
	if o.IdempotencyKey {
		_, _ = fmt.Fprintln(w, "\nA command: an Idempotency-Key is generated for each call; --dry-run shows the result without a change.")
	}
	_, _ = fmt.Fprintln(w, "\n"+globalHelp)
}

func wrap(s string, width int) string {
	var b strings.Builder
	n := 0
	for _, word := range strings.Fields(s) {
		if n > 0 && n+1+len(word) > width {
			b.WriteByte('\n')
			n = 0
		} else if n > 0 {
			b.WriteByte(' ')
			n++
		}
		b.WriteString(word)
		n += len(word)
	}
	return b.String()
}
