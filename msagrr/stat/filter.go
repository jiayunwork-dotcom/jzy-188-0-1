package stat

import (
	"errors"
	"sort"
)

var (
	// ErrUnbalancedInput indicates the balanced routine received data
	// whose shape does not match P*O*R.
	ErrUnbalancedInput = errors.New("balanced ANOVA requires P*O*R readings")
	// ErrNoUsableDesign indicates filtering leaves fewer than two
	// parts or fewer than one repeat per cell.
	ErrNoUsableDesign = errors.New("not enough complete data to run a crossed ANOVA")
)

// ExcludedReading explains why one raw reading did not enter the
// analysis.
type ExcludedReading struct {
	Part     string
	Operator string
	Trial    int
	Value    float64
	Reason   string
}

// MissingCell documents a (part, operator, trial) slot of the balanced
// design that had no reading at all.
type MissingCell struct {
	Part     string
	Operator string
	Trial    int
}

// CompleteCaseResult is the output of the generalized complete-case
// filter. The kept readings form a perfectly balanced P x O x R design.
type CompleteCaseResult struct {
	Parts     []string // parts kept, sorted ascending
	Operators []string // operators kept, sorted ascending
	R         int      // common repeats per cell
	// Matrix[partIndex][operatorIndex][trialIndex].
	Matrix   [][][]float64
	Excluded []ExcludedReading
	Missing  []MissingCell
}

// FilterCompleteCases turns an arbitrary set of crossed readings into a
// balanced design by:
//
//  1. keeping only parts measured by every operator (parts measured by
//     just some operators are dropped entirely),
//  2. keeping only operators who measured at least one retained part —
//     effectively all operators who measured every retained part,
//  3. within each retained cell keeping only trial numbers present in
//     every retained cell (the intersection of trial sets), so every
//     cell has the same R repeats; surplus repeats are excluded.
//
// The original labels are preserved in Parts/Operators and the filter
// reports every excluded raw reading and every missing slot, so the
// result always states exactly which data were used.
//
// Duplicate (same part/operator/trial) readings must be rejected by the
// caller before reaching this function.
func FilterCompleteCases(readings []struct {
	Part     string
	Operator string
	Trial    int
	Value    float64
}) (*CompleteCaseResult, error) {
	if len(readings) == 0 {
		return nil, ErrNoUsableDesign
	}

	// Which operators measured each part (with at least one reading).
	opsByPart := map[string]map[string]bool{}
	partsByOp := map[string]map[string]bool{}
	allOps := map[string]bool{}
	for _, rd := range readings {
		if opsByPart[rd.Part] == nil {
			opsByPart[rd.Part] = map[string]bool{}
		}
		if partsByOp[rd.Operator] == nil {
			partsByOp[rd.Operator] = map[string]bool{}
		}
		opsByPart[rd.Part][rd.Operator] = true
		partsByOp[rd.Operator][rd.Part] = true
		allOps[rd.Operator] = true
	}
	allOpLabels := make([]string, 0, len(allOps))
	for o := range allOps {
		allOpLabels = append(allOpLabels, o)
	}
	sort.Strings(allOpLabels)

	// Retained parts: those seen by every operator that appears in the
	// data. This mirrors "剔除不完整的零件" — a part one operator never
	// touched is the incomplete unit.
	var keptParts []string
	droppedParts := map[string]bool{}
	partList := make([]string, 0, len(opsByPart))
	for p := range opsByPart {
		partList = append(partList, p)
	}
	sort.Strings(partList)
	for _, p := range partList {
		complete := true
		for _, o := range allOpLabels {
			if !opsByPart[p][o] {
				complete = false
				break
			}
		}
		if complete {
			keptParts = append(keptParts, p)
		} else {
			droppedParts[p] = true
		}
	}
	if len(keptParts) < 2 {
		return nil, ErrNoUsableDesign
	}
	keptOps := allOpLabels
	keptOpSet := map[string]bool{}
	for _, o := range keptOps {
		keptOpSet[o] = true
	}

	// Bucket readings into cells of the retained design.
	type cellKey struct{ p, o string }
	cells := map[cellKey]map[int]float64{}
	var excluded []ExcludedReading
	for _, rd := range readings {
		if droppedParts[rd.Part] {
			excluded = append(excluded, ExcludedReading{
				Part: rd.Part, Operator: rd.Operator, Trial: rd.Trial,
				Value: rd.Value, Reason: "part not measured by every operator; part dropped",
			})
			continue
		}
		if _, ok := keptOpSet[rd.Operator]; !ok {
			excluded = append(excluded, ExcludedReading{
				Part: rd.Part, Operator: rd.Operator, Trial: rd.Trial,
				Value: rd.Value, Reason: "operator dropped from balanced design",
			})
			continue
		}
		k := cellKey{rd.Part, rd.Operator}
		if cells[k] == nil {
			cells[k] = map[int]float64{}
		}
		cells[k][rd.Trial] = rd.Value
	}

	// Common trial numbers across every retained cell.
	var common []int
	first := true
	for _, p := range keptParts {
		for _, o := range keptOps {

			trials := make([]int, 0, len(cells[cellKey{p, o}]))
			for t := range cells[cellKey{p, o}] {
				trials = append(trials, t)
			}
			sort.Ints(trials)
			if first {
				common = trials
				first = false
			} else {
				common = intersectSorted(common, trials)
			}
		}
	}
	if len(common) == 0 {
		return nil, ErrNoUsableDesign
	}
	R := len(common)

	// Build the balanced matrix and report surplus repeats.
	matrix := make([][][]float64, len(keptParts))
	for i, p := range keptParts {
		matrix[i] = make([][]float64, len(keptOps))
		for j, o := range keptOps {
			cell := cells[cellKey{p, o}]
			row := make([]float64, R)
			for k, t := range common {
				row[k] = cell[t]
			}
			matrix[i][j] = row
			commonSet := map[int]bool{}
			for _, t := range common {
				commonSet[t] = true
			}
			var surplus []int
			for t := range cell {
				if !commonSet[t] {
					surplus = append(surplus, t)
				}
			}
			sort.Ints(surplus)
			for _, t := range surplus {
				excluded = append(excluded, ExcludedReading{
					Part: p, Operator: o, Trial: t, Value: cell[t],
					Reason: "trial number not present in every part-operator cell; surplus repeat excluded",
				})
			}
		}
	}

	// Missing slots: relative to the largest repeat count observed in any
	// retained cell, list each absent slot of each retained cell. This
	// explains why R shrank (e.g. one cell missing its second trial),
	// while the readings dropped to match R appear in Excluded.
	maxObserved := 0
	for _, ts := range common {
		if ts > maxObserved {
			maxObserved = ts
		}
	}
	for _, p := range keptParts {
		for _, o := range keptOps {
			cell := cells[cellKey{p, o}]
			for t := range cell {
				if t > maxObserved {
					maxObserved = t
				}
			}
		}
	}
	var missing []MissingCell
	for _, p := range keptParts {
		for _, o := range keptOps {
			cell := cells[cellKey{p, o}]
			for t := 1; t <= maxObserved; t++ {
				if _, ok := cell[t]; !ok {
					missing = append(missing, MissingCell{Part: p, Operator: o, Trial: t})
				}
			}
		}
	}

	out := &CompleteCaseResult{
		Parts: keptParts, Operators: keptOps, R: R,
		Matrix: matrix, Excluded: excluded, Missing: missing,
	}
	return out, nil
}

func intersectSorted(a, b []int) []int {
	var out []int
	j := 0
	for _, x := range a {
		for j < len(b) && b[j] < x {
			j++
		}
		if j < len(b) && b[j] == x {
			out = append(out, x)
		}
	}
	return out
}

// Flatten returns the matrix as the row-major slice expected by
// ComputeBalancedANOVA.
func (c *CompleteCaseResult) Flatten() []float64 {
	P, O, R := len(c.Parts), len(c.Operators), c.R
	out := make([]float64, 0, P*O*R)
	for i := 0; i < P; i++ {
		for j := 0; j < O; j++ {
			out = append(out, c.Matrix[i][j][:R]...)
		}
	}
	return out
}
