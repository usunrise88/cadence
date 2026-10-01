package storage

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsRace(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"unique violation", &pgconn.PgError{Code: "23505"}, true},
		{"wrapped deadlock", fmt.Errorf("record worker: %w", &pgconn.PgError{Code: "40P01"}), true},
		{"serialization failure", &pgconn.PgError{Code: "40001"}, true},
		{"foreign key", &pgconn.PgError{Code: "23503"}, false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
		{"cancelled", context.Canceled, false},
	}
	for _, tt := range tests {
		if got := IsRace(tt.err); got != tt.want {
			t.Errorf("IsRace(%s) = %v; want %v", tt.name, got, tt.want)
		}
	}
}
