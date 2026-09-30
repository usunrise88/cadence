package diff3

import (
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

func lines(s string) []string { return Lines(s) }

func TestCompare(t *testing.T) {
	tests := []struct {
		name               string
		base, main, branch string
		want               []Hunk
	}{
		{
			name: "identical", base: "a\nb\n", main: "a\nb\n", branch: "a\nb\n",
			want: []Hunk{{Same, Range{0, 2}, Range{0, 2}, Range{0, 2}}},
		},
		{
			name: "branch edits one line", base: "a\nb\nc\n", main: "a\nb\nc\n", branch: "a\nB\nc\n",
			want: []Hunk{
				{Same, Range{0, 1}, Range{0, 1}, Range{0, 1}},
				{Branch, Range{1, 1}, Range{1, 1}, Range{1, 1}},
				{Same, Range{2, 1}, Range{2, 1}, Range{2, 1}},
			},
		},
		{
			name: "both edit different lines", base: "a\nb\nc\nd\n", main: "A\nb\nc\nd\n", branch: "a\nb\nc\nD\n",
			want: []Hunk{
				{Main, Range{0, 1}, Range{0, 1}, Range{0, 1}},
				{Same, Range{1, 2}, Range{1, 2}, Range{1, 2}},
				{Branch, Range{3, 1}, Range{3, 1}, Range{3, 1}},
			},
		},
		{
			name: "both edit the same line differently", base: "a\nb\nc\n", main: "a\nmain\nc\n", branch: "a\nbranch\nc\n",
			want: []Hunk{
				{Same, Range{0, 1}, Range{0, 1}, Range{0, 1}},
				{Conflict, Range{1, 1}, Range{1, 1}, Range{1, 1}},
				{Same, Range{2, 1}, Range{2, 1}, Range{2, 1}},
			},
		},
		{
			name: "both make the same change", base: "a\nb\n", main: "a\nx\n", branch: "a\nx\n",
			want: []Hunk{
				{Same, Range{0, 1}, Range{0, 1}, Range{0, 1}},
				{Both, Range{1, 1}, Range{1, 1}, Range{1, 1}},
			},
		},
		{
			name: "both append at the end", base: "a\n", main: "a\nm1\nm2\n", branch: "a\nb1\n",
			want: []Hunk{
				{Same, Range{0, 1}, Range{0, 1}, Range{0, 1}},
				{Conflict, Range{1, 0}, Range{1, 2}, Range{1, 1}},
			},
		},
		{
			name: "added on both sides", base: "", main: "x\n", branch: "y\n",
			want: []Hunk{{Conflict, Range{0, 0}, Range{0, 1}, Range{0, 1}}},
		},
		{
			name: "deleted on the branch, edited on main", base: "a\nb\n", main: "a\nB\n", branch: "",
			want: []Hunk{{Conflict, Range{0, 2}, Range{0, 2}, Range{0, 0}}},
		},
		{
			name: "insertion before a kept line", base: "a\nb\n", main: "a\nb\n", branch: "new\na\nb\n",
			want: []Hunk{
				{Branch, Range{0, 0}, Range{0, 0}, Range{0, 1}},
				{Same, Range{0, 2}, Range{0, 2}, Range{1, 2}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Compare(lines(tt.base), lines(tt.main), lines(tt.branch))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Compare =\n%+v\nwant\n%+v", got, tt.want)
			}
			checkCover(t, got, lines(tt.base), lines(tt.main), lines(tt.branch))
		})
	}
}

// checkCover asserts the hunks cover every line of the three texts, in order, without gaps.
func checkCover(t *testing.T, hs []Hunk, base, main, branch []string) {
	t.Helper()
	i, a, b := 0, 0, 0
	for _, h := range hs {
		if h.Base.Start != i || h.Main.Start != a || h.Branch.Start != b {
			t.Fatalf("hunk %+v does not start at %d/%d/%d", h, i, a, b)
		}
		if h.Kind == Same && !(equal(base[i:i+h.Base.Count], main[a:a+h.Main.Count]) && equal(base[i:i+h.Base.Count], branch[b:b+h.Branch.Count])) {
			t.Fatalf("same hunk %+v differs", h)
		}
		i, a, b = i+h.Base.Count, a+h.Main.Count, b+h.Branch.Count
	}
	if i != len(base) || a != len(main) || b != len(branch) {
		t.Fatalf("hunks end at %d/%d/%d, want %d/%d/%d", i, a, b, len(base), len(main), len(branch))
	}
}

func lcs(x, y []string) int {
	dp := make([][]int, len(x)+1)
	for i := range dp {
		dp[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	return dp[0][0]
}

func TestMatchIsALongestCommonSubsequence(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	gen := func() []string {
		n := r.IntN(14)
		out := make([]string, n)
		for i := range out {
			out[i] = string(rune('a' + r.IntN(4)))
		}
		return out
	}
	for range 2000 {
		x, y := gen(), gen()
		m := match(x, y)
		n, last := 0, -1
		for i, j := range m {
			if j < 0 {
				continue
			}
			if j <= last || x[i] != y[j] {
				t.Fatalf("match(%v, %v) = %v: not an increasing matching", x, y, m)
			}
			last = j
			n++
		}
		if want := lcs(x, y); n != want {
			t.Fatalf("match(%v, %v) keeps %d lines, the LCS is %d", x, y, n, want)
		}
	}
}

func TestCompareCoversRandomTexts(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	gen := func() []string {
		n := r.IntN(20)
		out := make([]string, n)
		for i := range out {
			out[i] = string(rune('a' + r.IntN(5)))
		}
		return out
	}
	for range 2000 {
		o, a, b := gen(), gen(), gen()
		checkCover(t, Compare(o, a, b), o, a, b)
	}
}

func TestLargeRewriteStaysBounded(t *testing.T) {
	var x, y strings.Builder
	for i := range 3000 {
		x.WriteString("x" + string(rune('0'+i%10)) + "\n")
		y.WriteString("y" + string(rune('0'+i%10)) + "\n")
	}
	hs := Compare(Lines(x.String()), Lines(x.String()), Lines(y.String()))
	if len(hs) != 1 || hs[0].Kind != Branch {
		t.Fatalf("a whole-file rewrite on the branch = %+v", hs)
	}
}
