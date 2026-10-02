package experiments

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// Param is one swept parameter (the contract's SweepParameter): values to try, or for a random sweep a numeric
// range drawn on a linear or log scale.
type Param struct {
	Name    string   `json:"name"`
	Values  []any    `json:"values,omitempty"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Scale   string   `json:"scale,omitempty"`
	Integer bool     `json:"integer,omitempty"`
}

// Scales of a random range.
const (
	ScaleLinear = "linear"
	ScaleLog    = "log"
)

// Points generates a sweep's points in order. grid: every combination of the parameters' values, the first
// parameter varying slowest, cut to the first runs when runs > 0; random: runs points (randomRuns when 0), each
// parameter drawn from its values or its range with a PCG seeded by seed, so the same request draws the same points.
// More than maxRuns points is refused.
func Points(mode string, params []Param, runs, randomRuns, maxRuns, seed int) ([]map[string]any, error) {
	if err := checkParams(mode, params); err != nil {
		return nil, err
	}
	switch mode {
	case ModeGrid:
		total := 1
		for _, p := range params {
			total *= len(p.Values)
			if total > 1_000_000 {
				break
			}
		}
		n := total
		if runs > 0 && runs < n {
			n = runs
		}
		if n > maxRuns {
			return nil, problems.Validation([]problems.FieldError{{Path: "/runs", Message: fmt.Sprintf(
				"the grid has %d combinations and a sweep may queue at most %d runs (defaults.yaml sweeps.max_runs); list fewer values or set runs", total, maxRuns)}})
		}
		out := make([]map[string]any, 0, n)
		idx := make([]int, len(params))
		for len(out) < n {
			pt := make(map[string]any, len(params))
			for i, p := range params {
				pt[p.Name] = p.Values[idx[i]]
			}
			out = append(out, pt)
			for i := len(params) - 1; i >= 0; i-- { // odometer: the last parameter varies fastest
				idx[i]++
				if idx[i] < len(params[i].Values) {
					break
				}
				idx[i] = 0
			}
		}
		return out, nil
	case ModeRandom:
		n := runs
		if n <= 0 {
			n = randomRuns
		}
		if n > maxRuns {
			return nil, problems.Validation([]problems.FieldError{{Path: "/runs", Message: fmt.Sprintf(
				"%d runs; a sweep may queue at most %d (defaults.yaml sweeps.max_runs)", n, maxRuns)}})
		}
		rng := rand.New(rand.NewPCG(uint64(seed), 0x5eed)) //nolint:gosec // a reproducible draw, not a secret
		out := make([]map[string]any, 0, n)
		for range n {
			pt := make(map[string]any, len(params))
			for _, p := range params {
				pt[p.Name] = draw(rng, p)
			}
			out = append(out, pt)
		}
		return out, nil
	}
	return nil, problems.Validation([]problems.FieldError{{Path: "/mode", Message: fmt.Sprintf("%q is not grid or random", mode)}})
}

// draw is one random value of p: one of its values, else a number in [min, max] on its scale.
func draw(rng *rand.Rand, p Param) any {
	if len(p.Values) > 0 {
		return p.Values[rng.IntN(len(p.Values))]
	}
	lo, hi := *p.Min, *p.Max
	u := rng.Float64()
	var v float64
	if p.Scale == ScaleLog {
		v = math.Exp(math.Log(lo) + u*(math.Log(hi)-math.Log(lo)))
	} else {
		v = lo + u*(hi-lo)
	}
	if p.Integer {
		return math.Round(v)
	}
	return sig4(v)
}

// sig4 rounds v to four significant digits (a drawn learning rate reads 0.0001234, not 0.00012339871).
func sig4(v float64) float64 {
	r, err := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 4, 64), 64)
	if err != nil {
		return v
	}
	return r
}

// checkParams validates the parameter list for mode: unique names, values for a grid, values or a range for a
// random draw, a positive range for a log scale, numbers for replayShare.
func checkParams(mode string, params []Param) error {
	var fields []problems.FieldError
	fail := func(i int, field, format string, args ...any) {
		fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/parameters/%d%s", i, field), Message: fmt.Sprintf(format, args...)})
	}
	if len(params) == 0 {
		return problems.Validation([]problems.FieldError{{Path: "/parameters", Message: "name at least one parameter to sweep"}})
	}
	seen := map[string]bool{}
	for i, p := range params {
		switch {
		case p.Name == "":
			fail(i, "/name", "name the parameter")
		case seen[p.Name]:
			fail(i, "/name", "%s is listed twice", p.Name)
		}
		seen[p.Name] = true
		ranged := p.Min != nil || p.Max != nil
		switch {
		case mode == ModeGrid && len(p.Values) == 0:
			fail(i, "/values", "a grid sweep lists the values of every parameter")
		case mode == ModeGrid && ranged:
			fail(i, "/min", "min and max are for a random sweep; a grid lists values")
		case len(p.Values) > 0 && ranged:
			fail(i, "/min", "give values or a min–max range, not both")
		case mode == ModeRandom && len(p.Values) == 0 && (p.Min == nil || p.Max == nil):
			fail(i, "/values", "a random sweep draws from values or from a min–max range")
		case ranged && *p.Min > *p.Max:
			fail(i, "/max", "max %g is below min %g", *p.Max, *p.Min)
		case ranged && p.Scale == ScaleLog && *p.Min <= 0:
			fail(i, "/min", "a log scale needs min above zero")
		}
		if p.Scale != "" && p.Scale != ScaleLinear && p.Scale != ScaleLog {
			fail(i, "/scale", "%q is not linear or log", p.Scale)
		}
		if p.Name == ReplayShare {
			for j, v := range p.Values {
				if _, ok := number(v); !ok {
					fail(i, fmt.Sprintf("/values/%d", j), "replayShare is a fraction (a number), not %v", v)
				}
			}
		}
	}
	if len(fields) > 0 {
		return problems.Validation(fields)
	}
	return nil
}

// number reads a JSON number.
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
