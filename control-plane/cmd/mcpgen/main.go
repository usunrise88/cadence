// Command mcpgen checks api/openapi.yaml against the naming rules (R1) and the verb vocabulary, then writes the
// artefacts derived from it: the MCP tool manifest, the Go stubs for planned operations, the TypeScript
// operation table and the command table of the generated CLI (internal/cli, R34). It refuses to write anything while the contract has violations.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/usunrise88/cadence/control-plane/internal/contract"
)

func main() {
	spec := flag.String("spec", "../api/openapi.yaml", "OpenAPI contract")
	vocab := flag.String("vocab", "../api/vocabulary.yaml", "verb vocabulary")
	tools := flag.String("tools", "internal/mcp/tools.json", "MCP tool manifest to write")
	planned := flag.String("planned", "internal/api/planned.gen.go", "Go stubs for planned operations to write")
	ts := flag.String("ts", "../web/src/api/operations.gen.ts", "TypeScript operation table to write")
	cliTable := flag.String("cli", "internal/cli/operations.gen.go", "operation table of the generated CLI to write")
	check := flag.Bool("check", false, "only check the contract; write nothing")
	flag.Parse()

	if err := run(*spec, *vocab, *tools, *planned, *ts, *cliTable, *check); err != nil {
		fmt.Fprintln(os.Stderr, "mcpgen:", err)
		os.Exit(1)
	}
}

func run(spec, vocab, tools, planned, ts, cliTable string, check bool) error {
	c, err := contract.Load(spec, vocab)
	if err != nil {
		return err
	}
	if errs := c.Check(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "  ", e)
		}
		return fmt.Errorf("%d contract violation(s); nothing written", len(errs))
	}
	if check {
		return nil
	}
	toolsJSON, err := c.ToolsJSON()
	if err != nil {
		return err
	}
	plannedGo, err := c.PlannedGo()
	if err != nil {
		return fmt.Errorf("format planned stubs: %w", err)
	}
	cliGo, err := c.CLIGo()
	if err != nil {
		return fmt.Errorf("CLI table: %w", err)
	}
	for path, body := range map[string][]byte{tools: toolsJSON, planned: plannedGo, ts: c.OperationsTS(), cliTable: cliGo} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("mcpgen: %d operations, %d tools\n", len(c.Ops), len(c.Tools()))
	return nil
}
