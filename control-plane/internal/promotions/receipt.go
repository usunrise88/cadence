package promotions

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ReceiptPrefix starts the one line a delivery script prints when it ran to the end.
const ReceiptPrefix = "CADENCE-RECEIPT"

// Receipt is a parsed receipt line:
//
//	CADENCE-RECEIPT 1 <recordHash> <servedSha256> <ok>/<total> <hostname> <UTC time>
//
// servedSha256 is the SHA-256 of the installed model directory's manifest (ManifestSHA256 in internal/delivery).
type Receipt struct {
	Line         string
	RecordHash   string
	ServedSHA256 string
	SmokeOK      int
	SmokeTotal   int
	Host         string
	RanAt        time.Time
}

var (
	hexRe   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	smokeRe = regexp.MustCompile(`^(\d{1,4})/(\d{1,4})$`)
	hostRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)
)

// ParseReceipt finds the one receipt line in text (a person may paste the script's surrounding output too) and
// parses it. Two different receipt lines are refused: Cadence will not guess which one is meant.
func ParseReceipt(text string) (Receipt, error) {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if i := strings.Index(l, ReceiptPrefix+" "); i >= 0 {
			l = l[i:]
			if len(lines) == 0 || lines[0] != l {
				lines = append(lines, l)
			}
		}
	}
	switch {
	case len(lines) == 0:
		return Receipt{}, errors.New("no CADENCE-RECEIPT line: paste the last line deliver.sh printed (it prints none when it stops early)")
	case len(lines) > 1:
		return Receipt{}, fmt.Errorf("%d different CADENCE-RECEIPT lines: paste only the one of this record", len(lines))
	}
	f := strings.Fields(lines[0])
	if len(f) != 7 {
		return Receipt{}, fmt.Errorf("a receipt has 7 fields (CADENCE-RECEIPT 1 <recordHash> <servedSha256> <ok>/<total> <host> <UTC time>), this one %d", len(f))
	}
	if f[1] != "1" {
		return Receipt{}, fmt.Errorf("receipt version %q: this Cadence reads version 1", f[1])
	}
	r := Receipt{Line: strings.Join(f, " "), RecordHash: f[2], ServedSHA256: f[3], Host: f[5]}
	if !hexRe.MatchString(r.RecordHash) {
		return Receipt{}, fmt.Errorf("record hash %q is not 64 lower-case hex digits", r.RecordHash)
	}
	if !hexRe.MatchString(r.ServedSHA256) {
		return Receipt{}, fmt.Errorf("served hash %q is not 64 lower-case hex digits", r.ServedSHA256)
	}
	m := smokeRe.FindStringSubmatch(f[4])
	if m == nil {
		return Receipt{}, fmt.Errorf("smoke result %q is not <ok>/<total>", f[4])
	}
	r.SmokeOK, _ = strconv.Atoi(m[1])
	r.SmokeTotal, _ = strconv.Atoi(m[2])
	if r.SmokeOK > r.SmokeTotal {
		return Receipt{}, fmt.Errorf("smoke result %q passes more utterances than it ran", f[4])
	}
	if !hostRe.MatchString(r.Host) {
		return Receipt{}, fmt.Errorf("host %q is not a host name", r.Host)
	}
	t, err := time.Parse("2006-01-02T15:04:05Z", f[6])
	if err != nil {
		return Receipt{}, fmt.Errorf("time %q is not UTC as YYYY-MM-DDTHH:MM:SSZ", f[6])
	}
	r.RanAt = t
	return r, nil
}
