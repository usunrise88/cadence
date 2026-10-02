package workers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// Log levels, lowest first.
var levels = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// LogLine is one line of a job's log (the contract's JobLogLine; WorkerLogLine plus its line number).
type LogLine struct {
	Seq    int             `json:"seq,omitempty"`
	T      time.Time       `json:"t"`
	Level  string          `json:"level"`
	Msg    string          `json:"msg"`
	Fields json.RawMessage `json:"fields,omitempty"`
}

var jobIDRe = regexp.MustCompile(`^job_[0-9a-f-]{36}$`)

// logPath is the NDJSON file of a job's log.
func (s *Service) logPath(jobID string) (string, error) {
	if s.logDir == "" {
		return "", problems.NotImplemented.New("job logs are not configured on this control plane")
	}
	if !jobIDRe.MatchString(jobID) {
		return "", fmt.Errorf("job log: %q is not a job id", jobID)
	}
	return filepath.Join(s.logDir, jobID+".ndjson"), nil
}

// AppendLogs appends a batch of NDJSON log lines from the step on lease leaseID to its job's log and emits them on
// job.{id}.log (at most 200 lines per event; the rest are counted as dropped, the file keeps all).
func (s *Service) AppendLogs(ctx context.Context, c Caller, leaseID string, body io.Reader) error {
	var (
		jobID, projectID string
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := lockLease(ctx, tx, c, leaseID)
		if err != nil {
			return err
		}
		if l.State == LeaseReaped {
			return ended(l)
		}
		jobID, projectID = l.JobID, l.ProjectID
		return nil
	})
	if err != nil {
		return err
	}
	path, err := s.logPath(jobID)
	if err != nil {
		return err
	}
	// Bytes past MaxLogBytes are discarded rather than refused: one huge line must not stop the job's log for good.
	// The line the limit cuts is kept, truncated (parseLines).
	raw, err := io.ReadAll(io.LimitReader(body, MaxLogBytes))
	if err != nil {
		return problems.BadRequest.New("cannot read the log lines: %v", err)
	}
	_, _ = io.Copy(io.Discard, body)
	lines, err := parseLines(raw, s.now())
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return nil
	}
	if err := s.appendFile(path, jobID, lines); err != nil {
		return err
	}
	shown, dropped := lines, 0
	if len(shown) > maxEventLines {
		shown, dropped = shown[:maxEventLines], len(lines)-maxEventLines
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return events.Append(ctx, tx, jobs.System, nil, []events.Draft{{
			Topic: jobs.Topic(jobID) + ".log", Type: EventLog, ProjectID: projectID,
			Payload: map[string]any{"jobId": jobID, "lines": shown, "dropped": dropped},
		}})
	})
}

// parseLines parses a batch of NDJSON log lines. A line longer than MaxLogLineBytes is never refused (truncateLine).
func parseLines(raw []byte, now time.Time) ([]LogLine, error) {
	var (
		out    []LogLine
		fields []problems.FieldError
	)
	for i, b := range bytes.Split(raw, []byte("\n")) {
		b = bytes.TrimSpace(b)
		if len(b) == 0 {
			continue
		}
		if len(b) > MaxLogLineBytes {
			out = append(out, truncateLine(b, now))
			continue
		}
		var l LogLine
		if err := json.Unmarshal(b, &l); err != nil || l.T.IsZero() || l.Msg == "" {
			fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/lines/%d", i), Message: "a JSON object with t (RFC 3339) and msg"})
			continue
		}
		if l.Level == "" {
			l.Level = "info"
		}
		if _, ok := levels[l.Level]; !ok {
			fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/lines/%d/level", i), Message: "debug, info, warn or error"})
			continue
		}
		if len(l.Msg) > maxMsgBytes {
			l.Msg = l.Msg[:maxMsgBytes]
		}
		l.Seq = 0
		out = append(out, l)
		if len(fields) > 20 {
			break
		}
	}
	if len(fields) > 0 {
		return nil, problems.Validation(fields)
	}
	return out, nil
}

// truncateLine turns a line longer than MaxLogLineBytes into one that fits and says so: a valid line keeps its time
// and level, its msg is cut and its fields dropped when they alone are too long; anything else (a line cut by
// MaxLogBytes, invalid JSON) becomes a warn line stamped now whose msg is the start of the raw text. Either way the
// msg ends with "[truncated: the line had N bytes]".
func truncateLine(b []byte, now time.Time) LogLine {
	mark := fmt.Sprintf(" [truncated: the line had %d bytes]", len(b))
	var l LogLine
	if err := json.Unmarshal(b, &l); err == nil && !l.T.IsZero() && l.Msg != "" {
		if l.Level == "" {
			l.Level = "info"
		}
		if _, ok := levels[l.Level]; ok {
			if len(l.Msg) > maxMsgBytes {
				l.Msg = strings.ToValidUTF8(l.Msg[:maxMsgBytes], "")
			}
			l.Msg += mark
			if len(l.Fields) > MaxLogLineBytes-maxMsgBytes-1024 {
				l.Fields = nil
			}
			l.Seq = 0
			return l
		}
	}
	prefix := b[:min(len(b), maxMsgBytes)]
	return LogLine{T: now, Level: "warn", Msg: strings.ToValidUTF8(string(prefix), "") + mark}
}

// appendFile writes lines to the job's log under the service's log lock, numbering them.
func (s *Service) appendFile(path, jobID string, lines []LogLine) error {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	n, ok := s.logSeq[jobID]
	if !ok {
		var err error
		if n, err = countLines(path); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640) //nolint:gosec // path is built from a validated job id
	if err != nil {
		return fmt.Errorf("open job log: %w", err)
	}
	var buf bytes.Buffer
	for i := range lines {
		n++
		lines[i].Seq = n
		stored := lines[i]
		stored.Seq = 0 // the line number is the position in the file
		b, err := json.Marshal(stored)
		if err != nil {
			_ = f.Close()
			return fmt.Errorf("encode log line: %w", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		return fmt.Errorf("append job log: %w", err)
	}
	s.logSeq[jobID] = n
	return f.Close()
}

func countLines(path string) (int, error) {
	f, err := os.Open(path) //nolint:gosec // as appendFile
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open job log: %w", err)
	}
	defer func() { _ = f.Close() }()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		n++
	}
	return n, sc.Err()
}

// LogFilter narrows ReadLogs.
type LogFilter struct {
	MinLevel string // "" for every level
	Text     string // case-insensitive substring of msg
	After    int    // only lines after this line number
	Tail     bool   // the last Limit matches instead of the first
	Limit    int
}

// ReadLogs returns a job's log lines matching f and the line number to continue after.
func (s *Service) ReadLogs(jobID string, f LogFilter) ([]LogLine, int, error) {
	path, err := s.logPath(jobID)
	if err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 {
		f.Limit = 500
	}
	minLevel := levels[f.MinLevel]
	text := strings.ToLower(f.Text)
	file, err := os.Open(path) //nolint:gosec // as appendFile
	if errors.Is(err, os.ErrNotExist) {
		return []LogLine{}, f.After, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("open job log: %w", err)
	}
	defer func() { _ = file.Close() }()
	out := []LogLine{}
	seq, next := 0, f.After
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		seq++
		if seq <= f.After {
			continue
		}
		next = seq
		var l LogLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if levels[l.Level] < minLevel || (text != "" && !strings.Contains(strings.ToLower(l.Msg), text)) {
			continue
		}
		l.Seq = seq
		out = append(out, l)
		if f.Tail {
			if len(out) > f.Limit {
				out = out[1:]
			}
		} else if len(out) == f.Limit {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, fmt.Errorf("read job log: %w", err)
	}
	return out, next, nil
}

// PruneLogs deletes job logs untouched for LogRetention (14 days); it reports how many it removed.
func (s *Service) PruneLogs(_ context.Context) (int, error) {
	if s.logDir == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(s.logDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("list job logs: %w", err)
	}
	cutoff := s.now().Add(-LogRetention)
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ndjson") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.logDir, e.Name())); err != nil {
			return n, fmt.Errorf("remove job log: %w", err)
		}
		s.logMu.Lock()
		delete(s.logSeq, strings.TrimSuffix(e.Name(), ".ndjson"))
		s.logMu.Unlock()
		n++
	}
	return n, nil
}
