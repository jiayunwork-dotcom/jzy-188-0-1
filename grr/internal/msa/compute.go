package msa

import (
	"fmt"
	"math"
	"sort"
)

// Validate checks incoming readings with field-level errors.
//   - values must be finite numbers
//   - trial >= 1
//   - part/operator labels non-empty
//   - at least 2 distinct parts and 2 distinct operators
//   - (part, operator, trial) unique
func Validate(data []Measurement, tolerance float64) *ValidationError {
	ve := &ValidationError{}
	add := func(field, msg string) {
		ve.Errors = append(ve.Errors, FieldError{Field: field, Message: msg})
	}
	if !finite(tolerance) || tolerance <= 0 {
		add("tolerance", "tolerance must be a positive finite number")
	}
	if len(data) == 0 {
		add("measurements", "at least one reading is required")
		return ve
	}
	parts, ops := map[string]bool{}, map[string]bool{}
	type trip struct {
		p, o string
		t    int
	}
	seen := map[trip]int{}
	for i, m := range data {
		f := fmt.Sprintf("measurements[%d]", i)
		if m.PartID == "" {
			add(f+".part_id", "part_id is required")
		}
		if m.OperatorID == "" {
			add(f+".operator_id", "operator_id is required")
		}
		if m.Trial < 1 {
			add(f+".trial", "trial must be a 1-based positive integer")
		}
		if !finite(m.Value) {
			add(f+".value", "value must be a finite number")
		}
		if m.PartID != "" {
			parts[m.PartID] = true
		}
		if m.OperatorID != "" {
			ops[m.OperatorID] = true
		}
		k := trip{m.PartID, m.OperatorID, m.Trial}
		if first, dup := seen[k]; dup {
			add(f, fmt.Sprintf("duplicate reading for part=%q operator=%q trial=%d (first at index %d)",
				m.PartID, m.OperatorID, m.Trial, first))
		} else {
			seen[k] = i
		}
	}
	if len(parts) < 2 {
		add("parts", "at least 2 distinct parts are required")
	}
	if len(ops) < 2 {
		add("operators", "at least 2 distinct operators are required")
	}
	if len(ve.Errors) > 0 {
		return ve
	}
	return nil
}

// Analyze runs the Gage R&R study.
//
// MethodAuto (default): if the retained data are perfectly balanced the
// classical closed-form ANOVA is used; otherwise the Henderson Method I
// projection engine runs on all readings.
//
// MethodBalancedDrop: incomplete parts are dropped and the remaining balanced
// subset is analyzed by the classical ANOVA.
//
// Interaction with p > 0.25 is folded into repeatability and the model is
// refit (both routes). Negative variance component estimates are reported at
// zero with a flag.
func Analyze(data []Measurement, tolerance float64, method Method) (*Result, error) {
	if ve := Validate(data, tolerance); ve != nil {
		return nil, ve
	}
	if method == "" {
		method = MethodAuto
	}

	var removed []RemovedReading
	used := data
	switch method {
	case MethodBalancedDrop:
		var parts []string
		var n int
		used, removed, parts, _, n = dropToBalanced(data)
		if n < 2 || len(parts) < 2 {

			return nil, &ValidationError{Errors: []FieldError{{
				Field:   "measurements",
				Message: "balanced_drop requires at least 2 parts each measured by every operator with the same repeat count >= 2",
			}}}
		}
	case MethodAuto:
	default:
		return nil, &ValidationError{Errors: []FieldError{{
			Field: "method", Message: "method must be henderson or balanced_drop",
		}}}
	}

	if lay, ok := detectBalanced(used); ok {
		return analyzeBalanced(used, lay, tolerance, removed, method), nil
	}
	if method == MethodBalancedDrop {
		// Defensive: dropping must always yield a balanced design.
		return nil, &ValidationError{Errors: []FieldError{{
			Field:   "measurements",
			Message: "internal: balanced_drop failed to produce a balanced design",
		}}}
	}
	return analyzeUnbalanced(used, tolerance, removed), nil
}

func analyzeBalanced(all []Measurement, lay *layout, tolerance float64, removed []RemovedReading, method Method) *Result {
	fitFull := balancedANOVA(lay, false)
	pooled := false
	var fit *balResult
	poolReason := ""
	if irow := findRow(fitFull.rows, "interaction"); irow.P != nil && *irow.P > 0.25 {
		pooled = true
		poolReason = fmt.Sprintf("interaction p = %.4f > 0.25; interaction SS and df merged into repeatability and model refit", *irow.P)
		fit = balancedANOVA(lay, true)
	} else {
		fit = fitFull
	}

	comps := orderedComponents(func(name string) (vcInfo, bool) {
		v, ok := fit.rawComps[name]
		return vcInfo{value: v, estimable: true}, ok
	}, pooled)

	rows := fit.rows
	res := &Result{
		Tolerance: tolerance,
		ANOVA:     rows,
		VarComps:  comps,
	}
	res.Metrics = buildMetrics(comps, tolerance)
	res.DataUsage = buildDataUsage(all, removed, method, true, pooled, poolReason,
		"balanced closed-form two-factor ANOVA with replication",
		fitNote(pooled))
	res.SSCheck = partitionCheck(rows)
	return res
}

func fitNote(pooled bool) []string {
	if !pooled {
		return nil
	}
	return []string{"interaction pooled into repeatability (p > 0.25 rule, AIAG MSA)"}
}

func analyzeUnbalanced(data []Measurement, tolerance float64, removed []RemovedReading) *Result {
	d := buildDesign(data)
	full := d.fit(false)
	pooled := false
	poolReason := ""
	irow := findFitRow(full.rows, "interaction")
	repRow := findFitRow(full.rows, "repeatability")
	repTestable := repRow.DF > 0 && repRow.MS != nil && *repRow.MS > 0
	switch {
	case irow.DF == 0 || irow.P == nil || !repTestable:
		// Interaction is not separately estimable/testable for this
		// missingness pattern (e.g. every cell measured only once).
		pooled = true
		poolReason = "interaction is not testable against repeatability for this pattern (no within-cell residual); merged into repeatability"
	case *irow.P > 0.25:
		pooled = true
		poolReason = fmt.Sprintf("interaction p = %.4f > 0.25; interaction space merged into repeatability and model refit", *irow.P)
	}
	var f *fit
	if pooled {
		f = d.fit(true)
	} else {
		f = full
	}

	// Recompute components in the same place solveComponents stores them.
	comps := fitComponents(f, pooled)

	res := &Result{
		Tolerance: tolerance,
		ANOVA:     f.rows,
		VarComps:  orderedFromInfo(comps, pooled),
	}
	res.Metrics = buildMetrics(res.VarComps, tolerance)
	res.DataUsage = buildDataUsage(data, removed, MethodAuto, false, pooled, poolReason,
		"Henderson Method I sequential sums of squares; expected mean squares via projection-matrix traces (valid for arbitrary missingness)",
		append(fitNote(pooled), unbalancedNotes(f)...))
	res.SSCheck = partitionCheck(f.rows)
	return res
}

func fitComponents(f *fit, pooled bool) map[string]vcInfo {
	// Rebuild the extLine/msR inputs from the table so the component solver
	// stays in one place.
	ext := []extLine{}
	var msR float64
	for _, r := range f.rows {
		if r.Source == "repeatability" {
			msR = *r.MS
		} else if r.Source != "total" {
			ext = append(ext, extLine{
				name: r.Source, df: r.DF, ms: *r.MS,
				ems: [4]float64{r.EMS[0], r.EMS[1], r.EMS[2], r.EMS[3]},
			})
		}
	}
	return solveComponents(ext, msR, pooled)
}

func unbalancedNotes(f *fit) []string {
	var out []string
	for _, r := range f.rows {
		if r.NonEstimable {
			out = append(out, fmt.Sprintf("variance component for %q is not estimable from this pattern and is reported as zero", r.Source))
		}
	}
	return out
}

func orderedComponents(lookup func(string) (vcInfo, bool), pooled bool) []VarComp {
	names := []string{"part", "operator", "interact", "repeat"}
	out := make([]VarComp, 0, len(names))
	for _, n := range names {
		v, ok := lookup(n)
		if !ok {
			continue
		}
		out = append(out, toVarComp(displayName(n), v))
	}
	return out
}

func orderedFromInfo(m map[string]vcInfo, pooled bool) []VarComp {
	names := []string{"part", "operator", "interaction", "repeatability"}
	out := make([]VarComp, 0, len(names))
	for _, n := range names {
		v, ok := m[n]
		if !ok {
			continue
		}
		out = append(out, toVarComp(displayName(n), v))
	}
	return out
}

func displayName(n string) string {
	switch n {
	case "interact":
		return "interaction"
	case "repeat":
		return "repeatability"
	default:
		return n
	}
}

func toVarComp(name string, v vcInfo) VarComp {
	vc := VarComp{Name: name, Raw: v.value, Estimable: v.estimable}
	if v.estimable && v.value < 0 {
		vc.Estimate = 0
		vc.Clipped = true
	} else {
		vc.Estimate = v.value
	}
	return vc
}

func compValue(comps []VarComp, name string) (float64, bool) {
	for i := range comps {
		if comps[i].Name == name {
			if !comps[i].Estimable {
				return 0, false
			}
			return comps[i].Estimate, true
		}
	}
	return 0, false
}

// buildMetrics turns (possibly clipped) components into SDs, percentages and
// NDC. Percentages use the AIAG convention: contribution to total variance
// (σ²_x/σ²_TV) for %TV and 6σ_x / tolerance for %tol; the user-visible
// "relative total" %GRR = σ_GRR/σ_TV.
func buildMetrics(comps []VarComp, tolerance float64) Metrics {
	s2P, _ := compValue(comps, "part")
	s2O, _ := compValue(comps, "operator")
	s2I, _ := compValue(comps, "interaction")
	s2E, _ := compValue(comps, "repeatability")
	s2Repro := s2O // operator-only reproducibility in this crossed design
	s2GRR := s2Repro + s2E
	s2TV := s2P + s2GRR + s2I

	sd := func(v float64) float64 { return math.Sqrt(math.Max(0, v)) }
	stat := func(name string, v float64) Stat {
		s := Stat{Name: name, Variance: v, SD: sd(v)}
		if s2TV > 0 {
			p := v / s2TV * 100
			s.PctTotalVariance = &p
			q := sd(v) / sd(s2TV) * 100
			s.PctStudyVariation = &q
		}
		if tolerance > 0 {
			p := 6 * sd(v) / tolerance * 100
			s.PctTolerance = &p
		}
		return s
	}

	m := Metrics{
		Repeatability:   stat("repeatability", s2E),
		Reproducibility: stat("reproducibility", s2Repro),
		GRR:             stat("grr", s2GRR),
		PartVariation:   stat("part_variation", s2P),
		TotalVariation:  stat("total_variation", s2TV),
	}
	if sd(s2TV) > 0 {
		m.PctGRROfTotal = sd(s2GRR) / sd(s2TV) * 100
	}
	if tolerance > 0 {
		m.PctGRROfTolerance = 6 * sd(s2GRR) / tolerance * 100
	}
	if sd(s2GRR) > 0 {
		ndc := 1.41 * sd(s2P) / sd(s2GRR)
		m.NDC = int(math.Floor(ndc + 1e-9))
	}
	return m
}

// partitionCheck audits that component SS add up to the total SS.
func partitionCheck(rows []ANOVARow) SSDiagnostic {
	var total, sum float64
	for _, r := range rows {
		if r.Source == "total" {
			total = r.SS
			continue
		}
		sum += r.SS
	}
	diff := math.Abs(total - sum)
	rel := 0.0
	if math.Abs(total) > 0 {
		rel = diff / math.Abs(total)
	}
	return SSDiagnostic{
		TotalSS: total, SumComponents: sum, Diff: diff, RelError: rel,
		WithinTol: rel <= 1e-9,
	}
}

func findRow(rows []ANOVARow, source string) ANOVARow {
	for _, r := range rows {
		if r.Source == source {
			return r
		}
	}
	return ANOVARow{}
}

func findFitRow(rows []ANOVARow, source string) ANOVARow { return findRow(rows, source) }

func buildDataUsage(all []Measurement, removed []RemovedReading, method Method,
	balanced, pooled bool, poolReason, reason string, notes []string) DataUsage {

	type key struct{ p, o string }
	cellN := map[key]int{}
	partSet, opSet := map[string]bool{}, map[string]bool{}
	trialSet := map[key]map[int]bool{}
	for _, m := range all {
		k := key{m.PartID, m.OperatorID}
		cellN[k]++
		partSet[m.PartID] = true
		opSet[m.OperatorID] = true
		if trialSet[k] == nil {
			trialSet[k] = map[int]bool{}
		}
		trialSet[k][m.Trial] = true
	}
	cells := make([]CellSummary, 0, len(cellN))
	common := -1
	for k, n := range cellN {
		cells = append(cells, CellSummary{PartID: k.p, OperatorID: k.o, Replicates: n})
		if common == -1 {
			common = n
		} else if common != n {
			common = 0
		}
	}
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].PartID != cells[j].PartID {
			return cells[i].PartID < cells[j].PartID
		}
		return cells[i].OperatorID < cells[j].OperatorID
	})
	if common == 0 {
		common = 0
	}
	return DataUsage{
		Method: method, MethodReason: reason,
		TotalReadings:     len(all) + len(removed),
		UsedReadings:      len(all),
		Removed:           removed,
		Parts:             sortedKeys(partSet),
		Operators:         sortedKeys(opSet),
		Cells:             cells,
		TrialsPerCell:     common,
		Balanced:          balanced,
		InteractionPooled: pooled,
		PoolReason:        poolReason,
		Notes:             notes,
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedStringSlice(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
