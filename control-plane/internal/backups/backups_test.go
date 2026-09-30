package backups

import (
	"strings"
	"testing"
)

func TestCheckMajor(t *testing.T) {
	for _, tc := range []struct {
		client string
		server int
		ok     bool
	}{
		{"17.6", 170006, true},
		{"18.0", 170006, true},
		{"15.13", 170006, false},
		{"17", 170000, true},
	} {
		err := checkMajor(tc.client, tc.server)
		if (err == nil) != tc.ok {
			t.Errorf("checkMajor(%s, %d) = %v", tc.client, tc.server, err)
		}
		if err != nil && !strings.Contains(err.Error(), "postgresql-client-17") {
			t.Errorf("the error should name the client to install: %v", err)
		}
	}
}

func TestSplitPasswordAndDatabase(t *testing.T) {
	dsn, pw, err := splitPassword("postgres://cadence:s3cret@postgres:5432/cadence?sslmode=disable")
	if err != nil || pw != "s3cret" || strings.Contains(dsn, "s3cret") || dsn != "postgres://cadence@postgres:5432/cadence?sslmode=disable" {
		t.Fatalf("splitPassword = %q %q %v", dsn, pw, err)
	}
	if dsn, pw, _ := splitPassword("host=x dbname=y"); dsn != "host=x dbname=y" || pw != "" {
		t.Fatalf("key=value DSN changed: %q %q", dsn, pw)
	}
	scratch, err := withDatabase("postgres://cadence:pw@db:5432/cadence?sslmode=disable", "cadence_restore_1")
	if err != nil || scratch != "postgres://cadence:pw@db:5432/cadence_restore_1?sslmode=disable" {
		t.Fatalf("withDatabase = %q %v", scratch, err)
	}
	if _, err := withDatabase("host=x", "y"); err == nil {
		t.Fatal("a key=value DSN cannot be retargeted")
	}
	if got := redactDSN("postgres://u:pw@h/db"); strings.Contains(got, "pw") {
		t.Fatalf("redactDSN = %q", got)
	}
}
