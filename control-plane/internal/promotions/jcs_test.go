package promotions

import (
	"math"
	"strings"
	"testing"
)

// RFC 8785 Appendix B: IEEE-754 bit patterns and the ECMAScript form each must take.
func TestFormatES6MatchesRFC8785AppendixB(t *testing.T) {
	cases := []struct {
		bits uint64
		want string
	}{
		{0x0000000000000000, "0"},
		{0x8000000000000000, "0"},
		{0x0000000000000001, "5e-324"},
		{0x8000000000000001, "-5e-324"},
		{0x7fefffffffffffff, "1.7976931348623157e+308"},
		{0xffefffffffffffff, "-1.7976931348623157e+308"},
		{0x4340000000000000, "9007199254740992"},
		{0xc340000000000000, "-9007199254740992"},
		{0x4430000000000000, "295147905179352830000"},
		{0x44b52d02c7e14af5, "9.999999999999997e+22"},
		{0x44b52d02c7e14af6, "1e+23"},
		{0x44b52d02c7e14af7, "1.0000000000000001e+23"},
		{0x444b1ae4d6e2ef4e, "999999999999999700000"},
		{0x444b1ae4d6e2ef4f, "999999999999999900000"},
		{0x444b1ae4d6e2ef50, "1e+21"},
		{0x3eb0c6f7a0b5ed8c, "9.999999999999997e-7"},
		{0x3eb0c6f7a0b5ed8d, "0.000001"},
		{0x41b3de4355555553, "333333333.3333332"},
		{0x41b3de4355555554, "333333333.33333325"},
		{0x41b3de4355555555, "333333333.3333333"},
		{0x41b3de4355555556, "333333333.3333334"},
		{0x41b3de4355555557, "333333333.33333343"},
		{0xbecbf647612f3696, "-0.0000033333333333333333"},
		{0x43143ff3c1cb0959, "1424953923781206.2"},
	}
	for _, c := range cases {
		if got := FormatES6(math.Float64frombits(c.bits)); got != c.want {
			t.Errorf("%016x: got %s, want %s", c.bits, got, c.want)
		}
	}
}

func TestCanonicalize(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			// RFC 8785 §3.2.4, the example of the whole transformation.
			name: "rfc8785 example",
			in: `{
				"numbers": [333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001],
				"string": "\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/",
				"literals": [null, true, false]
			}`,
			want: `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`,
		},
		{
			// RFC 8785 §3.2.3: member names sort by UTF-16 code units, so the emoji (a surrogate pair, D83D)
			// precedes U+FB33 although UTF-8 byte order puts it after.
			name: "rfc8785 sorting",
			in:   `{"\u20ac":"Euro Sign","\r":"Carriage Return","\ufb33":"Hebrew Letter Dalet With Dagesh","1":"One","\ud83d\ude00":"Emoji: Grinning Face","\u0080":"Control","\u00f6":"Latin Small Letter O With Diaeresis"}`,
			want: "{\"\\r\":\"Carriage Return\",\"1\":\"One\",\"\u0080\":\"Control\",\"\u00f6\":\"Latin Small Letter O With Diaeresis\",\"\u20ac\":\"Euro Sign\",\"\U0001F600\":\"Emoji: Grinning Face\",\"\ufb33\":\"Hebrew Letter Dalet With Dagesh\"}",
		},
		{name: "nested and empty", in: ` { "b" : [ {}, [], {"z":1,"a":-0} ], "a" : "" } `, want: `{"a":"","b":[{},[],{"a":0,"z":1}]}`},
		{name: "controls", in: `"\u0001\b\f\t\u001f\u007f"`, want: "\"\\u0001\\b\\f\\t\\u001f\u007f\""},
		{name: "integers", in: `[0, -1, 9007199254740992, 1.0, 100e-2]`, want: `[0,-1,9007199254740992,1,1]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Canonicalize([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
			again, err := Canonicalize(got)
			if err != nil || string(again) != string(got) {
				t.Errorf("canonical form is not a fixed point: %s (%v)", again, err)
			}
		})
	}
}

func TestCanonicalizeRefusesWhatIsNotIJSON(t *testing.T) {
	for _, in := range []string{
		`{"a":1,"a":2}`,           // duplicate member
		`[1e400]`,                 // beyond double precision
		`[9007199254740993]`,      // an integer a double cannot carry exactly
		`{"a":1} {"b":2}`,         // trailing data
		"[\"\xff\"]",              // invalid UTF-8
		`{"a":}`,                  // not JSON
		`[123456789012345678901]`, // beyond int64 and 2^53
	} {
		if _, err := Canonicalize([]byte(in)); err == nil {
			t.Errorf("Canonicalize(%q) accepted it", in)
		} else if !strings.HasPrefix(err.Error(), "jcs: ") {
			t.Errorf("error %v lacks the jcs prefix", err)
		}
	}
}
