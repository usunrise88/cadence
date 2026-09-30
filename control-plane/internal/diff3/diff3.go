// Package diff3 computes the line-level three-way comparison behind "Session changes" (docs/spec/05-agents.md
// "Worktree, drafts and merge"): the merge base, main and a branch are cut into hunks that cover every line of all
// three, each saying which side changed it. A conflict hunk is one both sides changed differently. The algorithm is
// the classic diff3 over two line diffs against the base (Khanna, Kunal and Pierce, "A Formal Investigation of
// Diff3", 2007); git's own merge decides whether a file conflicts, this package only lays the file out.
package diff3

import "strings"

// Kind is which side changed a hunk.
type Kind string

// The hunk kinds.
const (
	Same     Kind = "same"     // all three agree
	Main     Kind = "main"     // only main changed it: the merge takes main's lines
	Branch   Kind = "branch"   // only the branch changed it: the merge takes the branch's lines
	Both     Kind = "both"     // both made the same change
	Conflict Kind = "conflict" // both changed it, differently
)

// Range is a run of lines: Start is the 0-based index of the first line, Count how many.
type Range struct {
	Start int `json:"start"`
	Count int `json:"count"`
}

// Hunk is one region of the file in all three versions.
type Hunk struct {
	Kind   Kind  `json:"kind"`
	Base   Range `json:"base"`
	Main   Range `json:"main"`
	Branch Range `json:"branch"`
}

// Lines splits text into lines without their newlines; a final newline ends the last line rather than starting an
// empty one.
func Lines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// MaxEdits bounds the line diff: past this many inserted plus deleted lines the differing middle of the two texts
// is treated as one changed block (it keeps the work and memory of a huge rewrite small).
const MaxEdits = 2000

// Compare cuts base, main and branch into hunks that cover every line of each, in order.
func Compare(base, main, branch []string) []Hunk {
	ma := match(base, main)
	mb := match(base, branch)
	var out []Hunk
	i, a, b := 0, 0, 0
	for i < len(base) || a < len(main) || b < len(branch) {
		// A stable run: base lines that both sides kept where we are.
		s := 0
		for i+s < len(base) && ma[i+s] == a+s && mb[i+s] == b+s {
			s++
		}
		if s > 0 {
			out = append(out, Hunk{Kind: Same, Base: Range{i, s}, Main: Range{a, s}, Branch: Range{b, s}})
			i, a, b = i+s, a+s, b+s
			continue
		}
		// An unstable run: up to the next base line both sides kept (or the ends).
		j := i
		for j < len(base) && (ma[j] < 0 || mb[j] < 0) {
			j++
		}
		na, nb := len(main), len(branch)
		if j < len(base) {
			na, nb = ma[j], mb[j]
		}
		h := Hunk{Base: Range{i, j - i}, Main: Range{a, na - a}, Branch: Range{b, nb - b}}
		h.Kind = classify(base[i:j], main[a:na], branch[b:nb])
		out = append(out, h)
		i, a, b = j, na, nb
	}
	return out
}

func classify(o, a, b []string) Kind {
	switch ea, eb := equal(o, a), equal(o, b); {
	case ea && eb:
		return Same
	case ea:
		return Branch
	case eb:
		return Main
	case equal(a, b):
		return Both
	}
	return Conflict
}

func equal(x, y []string) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// Conflicts counts the conflict hunks.
func Conflicts(hs []Hunk) int {
	n := 0
	for _, h := range hs {
		if h.Kind == Conflict {
			n++
		}
	}
	return n
}

// match returns, for each line of x, the index of the line of y it is kept as in a shortest edit script, or -1.
// The pairs increase in both indexes.
func match(x, y []string) []int {
	m := make([]int, len(x))
	for i := range m {
		m[i] = -1
	}
	// Common prefix and suffix first: most edits are small and local.
	p := 0
	for p < len(x) && p < len(y) && x[p] == y[p] {
		m[p] = p
		p++
	}
	sx, sy := len(x), len(y)
	for sx > p && sy > p && x[sx-1] == y[sy-1] {
		sx--
		sy--
		m[sx] = sy
	}
	for _, pr := range myers(x[p:sx], y[p:sy]) {
		m[p+pr[0]] = p + pr[1]
	}
	return m
}

// myers returns the matched line pairs of a shortest edit script of x into y (Myers, "An O(ND) Difference
// Algorithm", 1986), or none when it needs more than MaxEdits edits.
func myers(x, y []string) [][2]int {
	n, m := len(x), len(y)
	if n == 0 || m == 0 {
		return nil
	}
	maxD := n + m
	if maxD > MaxEdits {
		maxD = MaxEdits
	}
	off := maxD + 1
	v := make([]int, 2*off+1)
	var trace [][]int
	for d := 0; d <= maxD; d++ {
		snap := make([]int, 2*d+1)
		copy(snap, v[off-d:off+d+1])
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var px int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				px = v[off+k+1] // down: an insertion
			} else {
				px = v[off+k-1] + 1 // right: a deletion
			}
			py := px - k
			for px < n && py < m && x[px] == y[py] {
				px++
				py++
			}
			v[off+k] = px
			if px >= n && py >= m {
				return backtrack(trace, x, y, d, k)
			}
		}
	}
	return nil
}

// backtrack walks the saved frontiers back from the end point on diagonal k after d edits.
func backtrack(trace [][]int, x, y []string, d, k int) [][2]int {
	var pairs [][2]int
	px, py := len(x), len(y)
	for ; d > 0; d-- {
		prev := trace[d] // the frontier before step d, diagonals -d … d at index k+d
		at := func(kk int) int { return prev[kk+d] }
		var pk int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		sx := at(pk)
		sy := sx - pk
		// The snake from the edit's end point to (px, py).
		ex, ey := sx, sy
		if pk == k+1 {
			ey++ // an insertion moved down
		} else {
			ex++ // a deletion moved right
		}
		for px > ex && py > ey {
			px--
			py--
			pairs = append(pairs, [2]int{px, py})
		}
		px, py, k = sx, sy, pk
	}
	for px > 0 && py > 0 {
		px--
		py--
		pairs = append(pairs, [2]int{px, py})
	}
	for i, j := 0, len(pairs)-1; i < j; i, j = i+1, j-1 {
		pairs[i], pairs[j] = pairs[j], pairs[i]
	}
	return pairs
}
