package promotions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Canonicalize returns the RFC 8785 (JSON Canonicalization Scheme) form of the JSON text in: object members sorted by
// the UTF-16 code units of their names, no insignificant whitespace, strings escaped as ECMAScript's JSON.stringify
// does, numbers in ECMAScript's Number.prototype.toString form. Input that is not I-JSON (duplicate member names,
// invalid UTF-8, numbers outside IEEE-754 double precision, trailing data) is refused.
func Canonicalize(in []byte) ([]byte, error) {
	if !utf8.Valid(in) { // encoding/json would replace invalid bytes with U+FFFD silently
		return nil, errors.New("jcs: the input is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, fmt.Errorf("jcs: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("jcs: trailing data after the JSON value")
	}
	var b bytes.Buffer
	if err := writeValue(&b, v); err != nil {
		return nil, fmt.Errorf("jcs: %w", err)
	}
	return b.Bytes(), nil
}

// CanonicalJSON marshals v with encoding/json and canonicalizes the result.
func CanonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("jcs: marshal: %w", err)
	}
	return Canonicalize(raw)
}

// member is one object member in input order.
type member struct {
	name  string
	value any
}

type object []member

// decodeValue reads one value: object (as object, duplicates refused), array, string, json.Number, bool or nil.
func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			var obj object
			seen := map[string]bool{}
			for dec.More() {
				nt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				name, ok := nt.(string)
				if !ok {
					return nil, fmt.Errorf("object member name %v is not a string", nt)
				}
				if seen[name] {
					return nil, fmt.Errorf("duplicate member name %q", name)
				}
				seen[name] = true
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				obj = append(obj, member{name: name, value: v})
			}
			if _, err := dec.Token(); err != nil { // '}'
				return nil, err
			}
			if obj == nil {
				obj = object{}
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil { // ']'
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	case string:
		if !utf8.ValidString(t) {
			return nil, errors.New("string is not valid UTF-8")
		}
		return t, nil
	default:
		return t, nil // json.Number, bool, nil
	}
}

func writeValue(b *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeString(b, t)
	case json.Number:
		s, err := formatNumber(string(t))
		if err != nil {
			return err
		}
		b.WriteString(s)
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeValue(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case object:
		sorted := append(object(nil), t...)
		sort.SliceStable(sorted, func(i, j int) bool { return lessUTF16(sorted[i].name, sorted[j].name) })
		b.WriteByte('{')
		for i, m := range sorted {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, m.name)
			b.WriteByte(':')
			if err := writeValue(b, m.value); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("unexpected value of type %T", v)
	}
	return nil
}

// lessUTF16 orders strings by their UTF-16 code units (RFC 8785 §3.2.3), which differs from Go's byte order for
// characters above U+FFFF against those in U+E000–U+FFFF.
func lessUTF16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// writeString escapes as ECMAScript's JSON.stringify: \" \\ \b \f \n \r \t, other controls as \u00xx (lower-case
// hex), everything else (including / and non-ASCII) literally.
func writeString(b *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hex[r>>4])
				b.WriteByte(hex[r&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// maxSafeInteger is 2^53: an integer literal beyond it cannot be carried exactly by a double.
const maxSafeInteger = 1 << 53

// formatNumber renders a JSON number literal as ECMAScript's Number.prototype.toString renders the double it
// denotes (RFC 8785 §3.2.2.3). Integer literals beyond ±2^53 are refused rather than silently rounded.
func formatNumber(lit string) (string, error) {
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil {
		return "", fmt.Errorf("number %s: %w", lit, err)
	}
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return "", fmt.Errorf("number %s is outside IEEE-754 double precision", lit)
	}
	if !strings.ContainsAny(lit, ".eE") {
		if n, err := strconv.ParseInt(strings.TrimPrefix(lit, "-"), 10, 64); err != nil || n > maxSafeInteger {
			return "", fmt.Errorf("integer %s is beyond 2^53 and cannot be carried exactly", lit)
		}
	}
	return FormatES6(f), nil
}

// FormatES6 formats a finite double as ECMAScript's Number.prototype.toString: the shortest digits that round-trip,
// plain notation for exponents in [-7, 21) and otherwise d[.ddd]e±n.
func FormatES6(f float64) string {
	if f == 0 {
		return "0" // also -0
	}
	neg := f < 0
	if neg {
		f = -f
	}
	// Shortest round-trip digits and decimal exponent: d.ddd × 10^exp.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expStr)
	k := len(digits)
	n := exp + 1 // the position of the decimal point relative to the digits (ECMAScript's n)
	var s string
	switch {
	case k <= n && n <= 21:
		s = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		s = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		s = "0." + strings.Repeat("0", -n) + digits
	default:
		sign := "+"
		if n-1 < 0 {
			sign = "-"
		}
		ex := n - 1
		if ex < 0 {
			ex = -ex
		}
		if k == 1 {
			s = digits + "e" + sign + strconv.Itoa(ex)
		} else {
			s = digits[:1] + "." + digits[1:] + "e" + sign + strconv.Itoa(ex)
		}
	}
	if neg {
		return "-" + s
	}
	return s
}
