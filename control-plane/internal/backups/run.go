package backups

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
)

// Config says where sets go and how Postgres is reached.
type Config struct {
	// Dir is CADENCE_BACKUP_DIR: sets/ and the content-store mirror cas/ live under it.
	Dir string
	// CASDir is the content store (CADENCE_CAS_DIR); empty skips the content-store copy.
	CASDir string
	// SecretsDir holds the sealed secret values; they are copied (still encrypted) into each set. Empty skips it.
	SecretsDir string
	// DSN is the database pg_dump and pg_restore connect to, as they see it (usually DATABASE_URL). Its password is
	// passed in PGPASSWORD, never on the command line.
	DSN string
	// PoolDSN is the same database as the control plane's pool sees it; the restore test connects to the scratch
	// database through it. Empty means DSN.
	PoolDSN string
	// PgDump and PgRestore are the commands (argv prefix); default pg_dump and pg_restore on PATH. Their major
	// version must be at least the server's.
	PgDump    []string
	PgRestore []string
}

func (c Config) pgDump() []string    { return orArgv(c.PgDump, "pg_dump") }
func (c Config) pgRestore() []string { return orArgv(c.PgRestore, "pg_restore") }

func orArgv(argv []string, def string) []string {
	if len(argv) == 0 {
		return []string{def}
	}
	return argv
}

// KeyTables are counted when a set is taken and compared after a restore.
var KeyTables = []string{
	"users", "credentials", "projects", "registry_collections", "registry_versions", "secrets", "approvals", "jobs",
	"audit_log", "mixes", "agent_sessions", "notification_rules", "backups",
	// Evaluation (phase 3): the records and metrics are shared caches every project's evals and gates read.
	"evals", "eval_cells", "eval_records", "eval_metrics",
}

// manifest is the set's manifest.json.
type manifest struct {
	ID               string       `json:"id"`
	Trigger          string       `json:"trigger"`
	TakenAt          time.Time    `json:"takenAt"`
	Dump             string       `json:"dump"`
	DumpBytes        int64        `json:"dumpBytes"`
	DumpSHA256       string       `json:"dumpSha256"`
	PgDumpVersion    string       `json:"pgDumpVersion"`
	ServerVersion    string       `json:"serverVersion"`
	MigrationVersion int64        `json:"migrationVersion"`
	Tables           []TableCount `json:"tables"`
	CASBlobs         int64        `json:"casBlobs"`
	CASCopied        int64        `json:"casCopied"`
	SecretsCopied    int          `json:"secretsCopied"`
	CASBytesCopied   int64        `json:"casBytesCopied"`
}

// DumpFile is the dump's name inside a set.
const DumpFile = "cadence.dump"

// Run takes set id: dump, content-store copy, sealed secrets, manifest. A failure marks the set failed (event
// backup.failed, which notifies) and removes its partial files. Retention runs after a success.
func (s *Service) Run(ctx context.Context, id string) (Backup, error) {
	b, err := s.update(ctx, id, EventStarted, `UPDATE backups SET state = 'running', started_at = $2, error = NULL,
		rev = rev + 1 WHERE id = $1`, s.now())
	if err != nil {
		return Backup{}, err
	}
	dir := filepath.Join(s.Config.Dir, "sets", b.CreatedAt.UTC().Format("20060102T150405Z")+"-"+b.Trigger+"-"+shortID(b.ID))
	m, err := s.take(ctx, b, dir)
	fctx := context.WithoutCancel(ctx)
	if err != nil {
		_ = os.RemoveAll(dir)
		fb, uerr := s.update(fctx, id, EventFailed, `UPDATE backups SET state = 'failed', finished_at = $2, error = $3,
			rev = rev + 1 WHERE id = $1`, s.now(), err.Error())
		if uerr != nil {
			return Backup{}, errors.Join(err, uerr)
		}
		return fb, err
	}
	tables, _ := json.Marshal(m.Tables)
	b, err = s.update(fctx, id, EventSucceeded, `UPDATE backups SET state = 'succeeded', finished_at = $2, path = $3,
		dump_bytes = $4, dump_sha256 = $5, pg_dump_version = $6, server_version = $7, migration_version = $8,
		tables = $9, cas_blobs = $10, cas_copied = $11, cas_bytes_copied = $12, rev = rev + 1 WHERE id = $1`,
		s.now(), dir, m.DumpBytes, m.DumpSHA256, m.PgDumpVersion, m.ServerVersion, m.MigrationVersion, tables,
		m.CASBlobs, m.CASCopied, m.CASBytesCopied)
	if err != nil {
		return Backup{}, err
	}
	if _, err := s.Prune(fctx); err != nil {
		s.Log.WarnContext(ctx, "backup retention failed", "err", err)
	}
	return b, nil
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "bkp_")
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

func (s *Service) take(ctx context.Context, b Backup, dir string) (manifest, error) {
	m := manifest{ID: b.ID, Trigger: b.Trigger, TakenAt: s.now().UTC(), Dump: DumpFile}
	if s.Config.Dir == "" {
		return m, errors.New("no backup directory is configured (CADENCE_BACKUP_DIR)")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return m, fmt.Errorf("create backup set directory: %w", err)
	}
	if err := s.dump(ctx, dir, &m); err != nil {
		return m, err
	}
	if err := s.copyCAS(ctx, &m); err != nil {
		return m, err
	}
	n, err := copySecrets(s.Config.SecretsDir, filepath.Join(dir, "secrets"))
	if err != nil {
		return m, err
	}
	m.SecretsCopied = n
	raw, _ := json.MarshalIndent(m, "", "  ")
	if err := writeFileAtomic(filepath.Join(dir, "manifest.json"), raw); err != nil {
		return m, err
	}
	return m, nil
}

// dump runs pg_dump on a snapshot exported by a repeatable-read transaction that also counts the key tables and
// reads the migration version, so the manifest describes exactly what the dump holds.
func (s *Service) dump(ctx context.Context, dir string, m *manifest) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var (
		snapshot  string
		serverNum int
	)
	if err := tx.QueryRow(ctx, `SELECT pg_export_snapshot(), current_setting('server_version'),
		current_setting('server_version_num')::int`).Scan(&snapshot, &m.ServerVersion, &serverNum); err != nil {
		return fmt.Errorf("export snapshot: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&m.MigrationVersion); err != nil {
		return fmt.Errorf("read the migration version: %w", err)
	}
	if m.Tables, err = countTables(ctx, tx); err != nil {
		return err
	}
	if m.PgDumpVersion, err = toolVersion(ctx, s.Config.pgDump()); err != nil {
		return err
	}
	if err := checkMajor(m.PgDumpVersion, serverNum); err != nil {
		return err
	}
	dsn, password, err := splitPassword(s.Config.DSN)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, DumpFile+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // our own set directory
	if err != nil {
		return fmt.Errorf("create dump file: %w", err)
	}
	h := sha256.New()
	counter := &countingWriter{}
	argv := append(append([]string{}, s.Config.pgDump()...), "--format=custom", "--snapshot="+snapshot, "--dbname="+dsn)
	stderr, err := runTool(ctx, argv, password, nil, io.MultiWriter(f, h, counter))
	if cerr := f.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("pg_dump: %w%s", err, tail(stderr))
	}
	if err := os.Rename(tmp, filepath.Join(dir, DumpFile)); err != nil {
		return fmt.Errorf("store dump: %w", err)
	}
	m.DumpBytes, m.DumpSHA256 = counter.n, hex.EncodeToString(h.Sum(nil))
	return nil
}

func countTables(ctx context.Context, q pgx.Tx) ([]TableCount, error) {
	var out []TableCount
	for _, t := range KeyTables {
		var exists bool
		if err := q.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+t).Scan(&exists); err != nil {
			return nil, fmt.Errorf("look up table %s: %w", t, err)
		}
		if !exists {
			continue
		}
		var n int64
		if err := q.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{t}.Sanitize()).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		out = append(out, TableCount{Name: t, Rows: n})
	}
	return out, nil
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// runTool runs argv with PGPASSWORD set (when password is not empty), stdin and stdout as given; it returns the
// tool's stderr.
func runTool(ctx context.Context, argv []string, password string, stdin io.Reader, stdout io.Writer) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // operator-configured tool, fixed arguments
	cmd.Env = os.Environ()
	if password != "" {
		cmd.Env = append(cmd.Env, "PGPASSWORD="+password)
	}
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, &stderr
	err := cmd.Run()
	return stderr.String(), err
}

func tail(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	if len(stderr) > 600 {
		stderr = "…" + stderr[len(stderr)-600:]
	}
	return ": " + stderr
}

var versionRe = regexp.MustCompile(`(\d+)(?:\.(\d+))?`)

// toolVersion runs `<tool> --version` and returns its version ("17.6").
func toolVersion(ctx context.Context, argv []string) (string, error) {
	var out bytes.Buffer
	stderr, err := runTool(ctx, append(append([]string{}, argv...), "--version"), "", nil, &out)
	if err != nil {
		return "", fmt.Errorf("%s --version: %w%s (install the PostgreSQL client matching the server)", strings.Join(argv, " "), err, tail(stderr))
	}
	v := versionRe.FindString(out.String())
	if v == "" {
		return "", fmt.Errorf("%s --version answered %q", strings.Join(argv, " "), strings.TrimSpace(out.String()))
	}
	return v, nil
}

// checkMajor refuses a client older than the server: pg_dump cannot dump a newer server.
func checkMajor(client string, serverNum int) error {
	major, _ := strconv.Atoi(strings.SplitN(client, ".", 2)[0])
	if major*10000 < serverNum-serverNum%10000 {
		return fmt.Errorf("pg_dump %s is older than the server (%d.x); install postgresql-client-%d", client, serverNum/10000, serverNum/10000)
	}
	return nil
}

// splitPassword removes the password from a postgres URL (it goes into PGPASSWORD instead of the command line).
// A key=value DSN is passed as it is.
func splitPassword(dsn string) (string, string, error) {
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return dsn, "", nil
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", "", fmt.Errorf("parse the database URL: %w", err)
	}
	if u.User == nil {
		return dsn, "", nil
	}
	password, _ := u.User.Password()
	u.User = url.User(u.User.Username())
	return u.String(), password, nil
}

// withDatabase returns dsn pointing at database db instead.
func withDatabase(dsn, db string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", fmt.Errorf("the restore test needs a postgres:// URL, not %q", redactDSN(dsn))
	}
	u.Path = "/" + db
	return u.String(), nil
}

func redactDSN(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && u.User != nil {
		u.User = url.User(u.User.Username())
		return u.String()
	}
	return "<dsn>"
}

// copyCAS mirrors content-store blobs that the mirror does not hold yet (blobs are immutable: an existing file of
// the same size is the same blob). Blobs that only training states hold are not mirrored: a state is read only to
// resume a run, it is the bulk of the store (7.66 GB each for the 0.6B model), and its eviction is permanent by design
// (docs/spec/07 open question D, decided 2026-10-01). A mirror on a mount (backups.mirror_mount) records every blob it
// holds as a copy on that mount.
func (s *Service) copyCAS(ctx context.Context, m *manifest) error {
	src := s.Config.CASDir
	if src == "" {
		return nil
	}
	mr, err := s.MirrorOf(ctx)
	if err != nil {
		return err
	}
	skip, err := stateOnlyBlobs(ctx, s.Pool)
	if err != nil {
		return err
	}
	root := filepath.Join(src, "b3")
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	dst := mr.Dir
	var copies []mounts.Copy
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk the content store: %w", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() || skip[d.Name()] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat blob: %w", err)
		}
		m.CASBlobs++
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return fmt.Errorf("blob path: %w", err)
		}
		target := filepath.Join(dst, rel)
		if mr.MountID != "" && blobName.MatchString(d.Name()) {
			copies = append(copies, mounts.Copy{Hash: cas.Prefix + d.Name(), Path: path.Join(mirrorRel, filepath.ToSlash(rel)), Size: info.Size()})
		}
		if st, err := os.Stat(target); err == nil && st.Size() == info.Size() {
			return nil
		}
		if err := copyFile(p, target); err != nil {
			return err
		}
		m.CASCopied++
		m.CASBytesCopied += info.Size()
		return nil
	})
	if err != nil {
		return err
	}
	return s.recordMirrored(ctx, mr, copies)
}

// blobName is a blob's file name in the store: the 64 hex digits of its hash.
var blobName = regexp.MustCompile(`^[0-9a-f]{64}$`)

// stateOnlyBlobs names (by their file name, the hex of the hash) the blobs that training-state artifacts hold — their
// manifests and listed files — and no other artifact does. A nil pool (tests without a database) skips nothing.
func stateOnlyBlobs(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	out := map[string]bool{}
	if pool == nil {
		return out, nil
	}
	rows, err := pool.Query(ctx, `WITH state_blobs AS (
			SELECT a.hash AS blob FROM artifacts a WHERE a.type = 'training-state'
			UNION SELECT f.file_hash FROM artifact_files f JOIN artifacts a ON a.hash = f.hash WHERE a.type = 'training-state')
		SELECT blob FROM state_blobs b
		WHERE NOT EXISTS (SELECT 1 FROM artifacts o WHERE o.hash = b.blob AND o.type <> 'training-state')
			AND NOT EXISTS (SELECT 1 FROM artifact_files f JOIN artifacts o ON o.hash = f.hash
				WHERE f.file_hash = b.blob AND o.type <> 'training-state')`)
	if err != nil {
		return nil, fmt.Errorf("training-state blobs: %w", err)
	}
	hashes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("training-state blobs: %w", err)
	}
	for _, h := range hashes {
		out[strings.TrimPrefix(h, cas.Prefix)] = true
	}
	return out, nil
}

func copySecrets(src, dst string) (int, error) {
	if src == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(src)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read the secret store: %w", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue // the transit area and temporary files are not stored secrets
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// copyFile copies src to dst through a temporary file and a rename, 0600.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}
	in, err := os.Open(src) //nolint:gosec // paths inside the content store, secret store and backup directory
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy %s: %w", src, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy %s: %w", src, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	return nil
}

func writeFileAtomic(p string, b []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}

// ---------------------------------------------------------------- restore test

// casSample is how many mirrored blobs a restore test re-hashes.
const casSample = 20

// RestoreTest restores set id into a scratch database, compares it with the set's manifest, re-hashes a sample of
// the content-store mirror and drops the scratch database. The report is stored on the set; a failed test emits
// backup.restore_failed (which notifies) and returns an error so the job fails too.
func (s *Service) RestoreTest(ctx context.Context, id string) (Backup, error) {
	b, err := Get(ctx, s.Pool, id)
	if err != nil {
		return Backup{}, err
	}
	rt := RestoreTest{State: "running", StartedAt: s.now()}
	if b.RestoreTest != nil {
		rt.StartedAt = b.RestoreTest.StartedAt
	}
	start := time.Now()
	terr := s.restore(ctx, b, &rt)
	fin := s.now()
	rt.FinishedAt, rt.DurationMS = &fin, time.Since(start).Milliseconds()
	typ := EventRestorePassed
	rt.State = "passed"
	if terr != nil {
		rt.State, rt.Error, typ = "failed", terr.Error(), EventRestoreFailed
	}
	raw, _ := json.Marshal(rt)
	nb, err := s.update(context.WithoutCancel(ctx), id, typ, `UPDATE backups SET restore_test = $2, rev = rev + 1 WHERE id = $1`, raw)
	if err != nil {
		return Backup{}, errors.Join(terr, err)
	}
	return nb, terr
}

func (s *Service) restore(ctx context.Context, b Backup, rt *RestoreTest) error {
	if b.State != StateSucceeded || b.Path == "" || b.PrunedAt != nil {
		return fmt.Errorf("backup set %s has no files to restore (%s)", b.ID, b.State)
	}
	dump := filepath.Join(b.Path, DumpFile)
	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	db := "cadence_restore_" + hex.EncodeToString(suffix[:])
	rt.Database = db
	toolDSN, err := withDatabase(s.Config.DSN, db)
	if err != nil {
		return err
	}
	poolDSN := s.Config.PoolDSN
	if poolDSN == "" {
		poolDSN = s.Config.DSN
	}
	if poolDSN, err = withDatabase(poolDSN, db); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()); err != nil {
		return fmt.Errorf("create the scratch database: %w", err)
	}
	defer func() {
		if _, err := s.Pool.Exec(context.WithoutCancel(ctx), "DROP DATABASE IF EXISTS "+pgx.Identifier{db}.Sanitize()+" WITH (FORCE)"); err != nil {
			s.Log.WarnContext(ctx, "restore test: drop the scratch database", "database", db, "err", err)
		}
	}()
	f, err := os.Open(dump) //nolint:gosec // the set's own dump
	if err != nil {
		return fmt.Errorf("open the dump: %w", err)
	}
	defer func() { _ = f.Close() }()
	dsn, password, err := splitPassword(toolDSN)
	if err != nil {
		return err
	}
	argv := append(append([]string{}, s.Config.pgRestore()...), "--no-owner", "--no-privileges", "--exit-on-error", "--dbname="+dsn)
	if stderr, err := runTool(ctx, argv, password, f, io.Discard); err != nil {
		return fmt.Errorf("pg_restore: %w%s", err, tail(stderr))
	}
	conn, err := pgx.Connect(ctx, poolDSN)
	if err != nil {
		return fmt.Errorf("connect to the scratch database: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if err := conn.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&rt.MigrationVersion); err != nil {
		return fmt.Errorf("read the restored migration version: %w", err)
	}
	var problems []string
	if b.MigrationVersion != nil && rt.MigrationVersion != *b.MigrationVersion {
		problems = append(problems, fmt.Sprintf("migration version %d, the set recorded %d", rt.MigrationVersion, *b.MigrationVersion))
	}
	for _, t := range b.Tables {
		var n int64
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{t.Name}.Sanitize()).Scan(&n); err != nil {
			problems = append(problems, fmt.Sprintf("table %s: %v", t.Name, err))
			continue
		}
		rt.Tables = append(rt.Tables, RestoreTable{Name: t.Name, BackedUp: t.Rows, Restored: n})
		if n != t.Rows {
			problems = append(problems, fmt.Sprintf("table %s has %d rows, the set recorded %d", t.Name, n, t.Rows))
		}
	}
	checked, err := s.checkCAS(ctx)
	rt.CASChecked = checked
	if err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// checkCAS re-hashes up to casSample mirrored blobs (the newest-looking first is not needed: any sample proves the
// mirror is readable and intact).
func (s *Service) checkCAS(ctx context.Context) (int, error) {
	mr, err := s.MirrorOf(ctx)
	if err != nil {
		return 0, err
	}
	root := filepath.Join(mr.Dir, "b3")
	n := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return filepath.SkipAll
		}
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if n >= casSample || ctx.Err() != nil {
			return filepath.SkipAll
		}
		f, err := os.Open(p) //nolint:gosec // inside the mirror
		if err != nil {
			return err
		}
		got, _, err := cas.HashReader(f)
		_ = f.Close()
		if err != nil {
			return err
		}
		n++
		if got != cas.Prefix+d.Name() {
			return fmt.Errorf("mirrored blob %s does not match its hash", d.Name())
		}
		return nil
	})
	return n, err
}
