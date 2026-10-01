// Command seedtraining puts what a training run needs besides a worker into the e2e stack's throwaway database, for
// the Playwright specs of the Run, Metrics and Checkpoints panels (phase 2 · stream U): the fixture family's base
// model, a frozen trainable dataset version whose artifact is in the content store, and a calibration of the base
// model on the seeded card so runs.new has a measured estimate. The fixture family and its step kinds are published by
// the spec's scripted worker through the worker protocol. It is test tooling: the e2e stack script builds it next to
// the control plane; it never ships in the image.
//
//	DATABASE_URL=… CADENCE_CAS_DIR=… seedtraining --dataset e2e-he
//
// It prints {"baseModel": …, "dataset": "ver_…"} on stdout. Running it again registers the same versions again.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "seedtraining:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("seedtraining", flag.ContinueOnError)
	dataset := fs.String("dataset", "e2e-he", "dataset collection name (dataset/<name>)")
	hours := fs.Float64("hours", 2, "train hours of the dataset version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dsn, dir := os.Getenv("DATABASE_URL"), os.Getenv("CADENCE_CAS_DIR")
	if dsn == "" || dir == "" {
		return errors.New("DATABASE_URL and CADENCE_CAS_DIR are required")
	}
	pool, err := storage.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	store, err := cas.New(dir)
	if err != nil {
		return err
	}
	if err := pipelinestest.RegisterBaseModel(ctx, pool); err != nil {
		return fmt.Errorf("base model: %w", err)
	}
	id, err := pipelinestest.RegisterDataset(ctx, pool, store, *dataset, *hours, false)
	if err != nil {
		return fmt.Errorf("dataset: %w", err)
	}
	// A measured estimate for the fixture base model on the seeded staging card (blackwell-48gb, 24 GB cap, bf16).
	if _, err := pool.Exec(ctx, `INSERT INTO calibrations (base_model, card_class, memory_cap_gb, precision, seconds_per_step,
			plus_minus, family, artifact_hash) VALUES ($1, 'blackwell-48gb', 24, 'bf16', 0.5, 0.1, $2, 'b3:e2e')
		ON CONFLICT DO NOTHING`, pipelinestest.BaseModel, pipelinestest.FamilyName); err != nil {
		return fmt.Errorf("calibration: %w", err)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"baseModel": pipelinestest.BaseModel, "dataset": id})
}
