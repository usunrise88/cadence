//go:build integration

// Package testdb gives integration tests a throwaway Postgres (testcontainers, postgres:17): one container per
// test binary, one fresh database per test.
package testdb

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var (
	adminDSN string
	counter  atomic.Int64
)

// Main starts the container, runs the tests and removes the container. Call it from TestMain.
func Main(m *testing.M) {
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:17",
		postgres.WithDatabase("cadence"), postgres.WithUsername("cadence"), postgres.WithPassword("cadence"),
		postgres.BasicWaitStrategies())
	if err != nil {
		log.Fatalf("start postgres: %v", err)
	}
	adminDSN, err = c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Fatalf("postgres dsn: %v", err)
	}
	code := m.Run()
	if err := testcontainers.TerminateContainer(c); err != nil {
		log.Printf("terminate postgres: %v", err)
	}
	os.Exit(code)
}

// New creates an empty database and returns its DSN.
func New(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	name := fmt.Sprintf("t_%d", counter.Add(1))
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	return strings.Replace(adminDSN, "/cadence?", "/"+name+"?", 1)
}
