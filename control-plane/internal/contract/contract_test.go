package contract

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	specPath  = "../../../api/openapi.yaml"
	vocabPath = "../../../api/vocabulary.yaml"
)

func TestRealContractPasses(t *testing.T) {
	c, err := Load(specPath, vocabPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range c.Check() {
		t.Error(e)
	}
}

// The generated files must be what mcpgen would write now (make gen was run and committed).
func TestGeneratedFilesInSync(t *testing.T) {
	c, err := Load(specPath, vocabPath)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := c.ToolsJSON()
	if err != nil {
		t.Fatal(err)
	}
	planned, err := c.PlannedGo()
	if err != nil {
		t.Fatal(err)
	}
	cli, err := c.CLIGo()
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]byte{
		"../cli/operations.gen.go":               cli,
		"../mcp/tools.json":                      tools,
		"../api/planned.gen.go":                  planned,
		"../../../web/src/api/operations.gen.ts": c.OperationsTS(),
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run make gen)", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale; run make gen", path)
		}
	}
}

// api/vocabulary.yaml is the machine copy of the spec's verb table; they must list the same verbs.
func TestVocabularyMatchesSpec(t *testing.T) {
	c, err := Load(specPath, vocabPath)
	if err != nil {
		t.Fatal(err)
	}
	md, err := os.ReadFile("../../../docs/spec/10-ui-shell.md")
	if err != nil {
		t.Fatal(err)
	}
	section := string(md)
	start := strings.Index(section, "### Verb vocabulary")
	end := strings.Index(section, "### Entity manifest")
	if start < 0 || end < start {
		t.Fatal("verb vocabulary section not found")
	}
	row := regexp.MustCompile(`(?m)^\| ([a-z, ]+) \|`)
	specVerbs := map[string]bool{}
	for _, m := range row.FindAllStringSubmatch(section[start:end], -1) {
		for _, v := range strings.Split(m[1], ",") {
			if v = strings.TrimSpace(v); v != "" && v != "verb" {
				specVerbs[v] = true
			}
		}
	}
	var missing, extra []string
	for v := range specVerbs {
		if _, ok := c.Vocab.Verbs[v]; !ok {
			missing = append(missing, v)
		}
	}
	for v := range c.Vocab.Verbs {
		if !specVerbs[v] {
			extra = append(extra, v)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing)+len(extra) > 0 {
		t.Errorf("vocabulary.yaml vs spec table: missing %v, extra %v", missing, extra)
	}
}

func TestNames(t *testing.T) {
	cases := []struct{ in, kebab, snakeSingular string }{
		{"goldenSets", "golden-sets", "golden_set"},
		{"projects", "projects", "project"},
		{"agentSessions", "agent-sessions", "agent_session"},
		{"batches", "batches", "batch"},
		{"registries", "registries", "registry"},
		{"mixes", "mixes", "mix"},
	}
	for _, tc := range cases {
		if got := Kebab(tc.in); got != tc.kebab {
			t.Errorf("Kebab(%s) = %s, want %s", tc.in, got, tc.kebab)
		}
		if got := Snake(Singular(tc.in)); got != tc.snakeSingular {
			t.Errorf("Snake(Singular(%s)) = %s, want %s", tc.in, got, tc.snakeSingular)
		}
	}
	if GoName("agentSessions.new") != "AgentSessionsNew" {
		t.Error("GoName")
	}
}

const header = `openapi: 3.1.0
info: { title: t, version: "1" }
paths:
`

const components = `
components:
  parameters:
    IdempotencyKey: { name: Idempotency-Key, in: header, required: true, schema: { type: string } }
    IfMatch: { name: If-Match, in: header, required: true, schema: { type: string } }
    IfMatchOptional: { name: If-Match, in: header, required: false, schema: { type: string } }
    DryRun: { name: dryRun, in: query, schema: { type: boolean } }
  responses:
    Problem: { description: e, content: { application/problem+json: { schema: { type: object } } } }
    JobAccepted: { description: j, content: { application/json: { schema: { type: object } } } }
`

const mut = `parameters: [{ $ref: "#/components/parameters/IdempotencyKey" }, { $ref: "#/components/parameters/DryRun" }%s]`

// Each case is one path item; the checker must reject it with a message containing want (or accept it if want is "").
func TestCheckRejects(t *testing.T) {
	ifm := `, { $ref: "#/components/parameters/IfMatch" }`
	resp := `responses: { "200": { description: ok }, default: { $ref: "#/components/responses/Problem" } }`
	cases := []struct{ name, path, want string }{
		{"ok list", `  /runs:
    x-cadence: { entity: runs }
    get: { operationId: runs.list, summary: s, tags: [x], ` + resp + ` }`, ""},
		{"ok action", `  /runs/{id}:cancel:
    x-cadence: { entity: runs }
    post: { operationId: runs.cancel, summary: s, tags: [x], ` + sprintf(mut, ifm) + `, ` + resp + ` }`, ""},
		{"camel opId", `  /runs:
    x-cadence: { entity: runs }
    get: { operationId: listRuns, summary: s, tags: [x], ` + resp + ` }`, "<entity>.<verb>"},
		{"unknown verb", `  /runs:
    x-cadence: { entity: runs }
    get: { operationId: runs.query, summary: s, tags: [x], ` + resp + ` }`, "not in api/vocabulary.yaml"},
		{"wrong shape", `  /runs:
    x-cadence: { entity: runs }
    post: { operationId: runs.create, summary: s, tags: [x], ` + sprintf(mut, "") + `, ` + resp + ` }`, "not in api/vocabulary.yaml"},
		{"post collection must be new", `  /runs:
    x-cadence: { entity: runs }
    post: { operationId: runs.edit, summary: s, tags: [x], ` + sprintf(mut, "") + `, ` + resp + ` }`, "HTTP shape"},
		{"entity mismatch", `  /runs:
    x-cadence: { entity: runs }
    get: { operationId: jobs.list, summary: s, tags: [x], ` + resp + ` }`, "differs from the path"},
		{"segment not kebab", `  /goldenSets:
    x-cadence: { entity: goldenSets }
    get: { operationId: goldenSets.list, summary: s, tags: [x], ` + resp + ` }`, "kebab plural"},
		{"action suffix mismatch", `  /runs/{id}:pause:
    x-cadence: { entity: runs }
    post: { operationId: runs.cancel, summary: s, tags: [x], ` + sprintf(mut, ifm) + `, ` + resp + ` }`, "must equal the verb"},
		{"missing idempotency", `  /runs/{id}:
    x-cadence: { entity: runs }
    patch: { operationId: runs.edit, summary: s, tags: [x], parameters: [{ $ref: "#/components/parameters/IfMatch" }], ` + resp + ` }`, "IdempotencyKey"},
		{"missing if-match", `  /runs/{id}:
    x-cadence: { entity: runs }
    patch: { operationId: runs.edit, summary: s, tags: [x], ` + sprintf(mut, "") + `, ` + resp + ` }`, "IfMatch"},
		{"delete", `  /runs/{id}:
    x-cadence: { entity: runs }
    delete: { operationId: runs.archive, summary: s, tags: [x], ` + sprintf(mut, ifm) + `, ` + resp + ` }`, "DELETE"},
		{"no problem default", `  /runs:
    x-cadence: { entity: runs }
    get: { operationId: runs.list, summary: s, tags: [x], responses: { "200": { description: ok } } }`, "Problem"},
		{"search needs q", `  /runs:
    x-cadence: { entity: runs }
    get: { operationId: runs.search, summary: s, tags: [x], ` + resp + ` }`, "HTTP shape"},
		{"read with dryRun", `  /runs:
    x-cadence: { entity: runs }
    get: { operationId: runs.list, summary: s, tags: [x], parameters: [{ $ref: "#/components/parameters/DryRun" }], ` + resp + ` }`, "read operations"},
		{"planned without tag", `  /runs:
    x-cadence: { entity: runs, planned: 2 }
    get: { operationId: runs.list, summary: s, tags: [x], ` + resp + ` }`, "planned tag"},
		{"no x-cadence", `  /runs:
    get: { operationId: runs.list, summary: s, tags: [x], ` + resp + ` }`, "x-cadence.entity"},
		{"exempt tag may use any verb", `  /sessions/{id}:login:
    x-cadence: { entity: sessions }
    post: { operationId: sessions.login, summary: s, tags: [auth], ` + sprintf(mut, ifm) + `, ` + resp + ` }`, ""},
		{"auth operations are not commands", `  /auth:login:
    x-cadence: { entity: auth, singleton: true }
    post: { operationId: auth.login, summary: s, tags: [auth], ` + resp + ` }`, ""},
		{"host operations are not commands", `  /host-sessions:claim:
    x-cadence: { entity: hostSessions }
    post: { operationId: hostSessions.claim, summary: s, tags: [host], ` + resp + ` }`, ""},
		{"me mutations stay commands", `  /me:login:
    x-cadence: { entity: me, singleton: true }
    post: { operationId: me.login, summary: s, tags: [me], ` + resp + ` }`, "IdempotencyKey"},
	}
	vocab, err := filepath.Abs(vocabPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "openapi.yaml")
			if err := os.WriteFile(f, []byte(header+tc.path+"\n"+components), 0o644); err != nil {
				t.Fatal(err)
			}
			c, err := Load(f, vocab)
			if err != nil {
				t.Fatal(err)
			}
			errs := c.Check()
			if tc.want == "" {
				for _, e := range errs {
					t.Error("unexpected:", e)
				}
				return
			}
			for _, e := range errs {
				if strings.Contains(e.Error(), tc.want) {
					return
				}
			}
			t.Errorf("want an error containing %q, got %v", tc.want, errs)
		})
	}
}

func sprintf(format string, a ...any) string {
	return strings.Replace(format, "%s", a[0].(string), 1)
}

// The CLI table names flags after parameters (p → --project, dryRun → --dry-run, If-Match → --if-match), leaves
// out the Idempotency-Key, and refuses a parameter whose flag another parameter or a global flag already has.
func TestCLIFlags(t *testing.T) {
	vocab, err := filepath.Abs(vocabPath)
	if err != nil {
		t.Fatal(err)
	}
	resp := `responses: { "200": { description: ok }, default: { $ref: "#/components/responses/Problem" } }`
	load := func(t *testing.T, path string) *Contract {
		t.Helper()
		f := filepath.Join(t.TempDir(), "openapi.yaml")
		if err := os.WriteFile(f, []byte(header+path+"\n"+components), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := Load(f, vocab)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	ok := load(t, `  /projects/{p}/runs/{runId}:cancel:
    x-cadence: { entity: runs }
    parameters: [{ name: p, in: path, required: true, schema: { type: string } }, { name: runId, in: path, required: true, schema: { type: string } }]
    post: { operationId: runs.cancel, summary: s, tags: [x], `+sprintf(mut, `, { $ref: "#/components/parameters/IfMatch" }`)+`, `+resp+` }`)
	src, err := ok.CLIGo()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Flag: "project"`, `Flag: "run-id"`, `Flag: "if-match"`, `Flag: "dry-run"`, "IdempotencyKey: true"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("CLI table lacks %s:\n%s", want, src)
		}
	}
	if strings.Contains(string(src), `"Idempotency-Key"`) {
		t.Error("Idempotency-Key must not be a flag")
	}
	for name, path := range map[string]string{
		"collides with --project": `  /projects/{p}/runs:
    x-cadence: { entity: runs }
    parameters: [{ name: p, in: path, required: true, schema: { type: string } }]
    get: { operationId: runs.list, summary: s, tags: [x], parameters: [{ name: project, in: query, schema: { type: string } }], ` + resp + ` }`,
		"collides with a global flag": `  /runs:
    x-cadence: { entity: runs }
    get: { operationId: runs.list, summary: s, tags: [x], parameters: [{ name: token, in: query, schema: { type: string } }], ` + resp + ` }`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := load(t, path).CLIGo(); err == nil || !strings.Contains(err.Error(), "already") {
				t.Errorf("CLIGo = %v, want a collision error", err)
			}
		})
	}
}

// Media operations (tag media, phase 3 · stream A) are exempt from the vocabulary, never MCP tools and never CLI
// commands; an utterance's media action (audio.sign) is not a command.
func TestMediaOperationsExempt(t *testing.T) {
	c, err := Load(specPath, vocabPath)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := c.ToolsJSON()
	if err != nil {
		t.Fatal(err)
	}
	cli, err := c.CLIGo()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"audio.get": true, "audio.sign": true, "peaks.get": true, "spectrogram.get": true, "words.get": true,
		"transcriptions.new": true, "stream.connect": true}
	for _, o := range c.Ops {
		if !hasTag(o, "media") || !want[o.ID] {
			continue
		}
		delete(want, o.ID)
		if !o.Exempt {
			t.Errorf("%s is tagged media but not exempt", o.ID)
		}
		if bytes.Contains(tools, []byte(`"`+o.ID+`"`)) {
			t.Errorf("%s is an MCP tool", o.ID)
		}
		if bytes.Contains(cli, []byte(`"`+o.ID+`"`)) {
			t.Errorf("%s is a CLI command", o.ID)
		}
	}
	for id := range want {
		t.Errorf("%s is missing from the contract (or not tagged media)", id)
	}
}
