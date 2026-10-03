package msa

import (
	"math"
	"math/rand"
	"testing"
)

// Hand-built 10 parts x 3 operators x 3 trials fixture.
//
//	y_ijk = a_i + b_j + s_i*t_j + e_ijk
//
//	a_i = i (0..9), b_j = j (0,1,2), s_i = i-4.5, t_j = j-1,
//	e cycles -1,0,1 within each cell.
//
// Every term is orthogonal in the balanced layout:
//
//	SSpart = o*n*Σ(a_i-mean)² = 9 * 82.5     = 742.5
//	SSop   = p*n*Σ(b_j-mean)² = 30 * 2       = 60
//	SSint  = n*Σ_i s_i² * Σ_j t_j² = 3*82.5*2 = 495
//	SSrep  = Σ e² (60 cells, 2 per cell)      = 60
//	SStot  = 1357.5
//
// MSI = 495/18 = 27.5, MSE = 1, F_int = 27.5 (tiny p, no pooling).
// Components: repeat 1, interaction (27.5-1)/3 = 8.8333...,
// operator (30-27.5)/30 = 1/12 (positive), part (82.5-27.5)/9 = 55/9.

func fixtureData() []Measurement {
	data := make([]Measurement, 0, 90)
	errs := []float64{-1, 0, 1}
	for i := 0; i < 10; i++ {
		a := float64(i)
		s := float64(i) - 4.5
		for j := 0; j < 3; j++ {
			b := float64(j)
			t := float64(j - 1)
			for k := 0; k < 3; k++ {
				y := a + b + s*t + errs[k]
				data = append(data, Measurement{
					PartID: partLabel(i), OperatorID: opLabel(j),
					Trial: k + 1, Value: y,
				})
			}
		}
	}
	return data
}

func partLabel(i int) string {
	return "P" + string(rune('0'+i))
}
func opLabel(j int) string {
	return "O" + string(rune('0'+j))
}

const tolFixture = 10.0

func rowBySource(rows []ANOVARow, s string) ANOVARow {
	for _, r := range rows {
		if r.Source == s {
			return r
		}
	}
	return ANOVARow{}
}

func approxEq(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %.10f, want %.10f (diff %.3e)", name, got, want, got-want)
	}
}

func TestFixtureBalancedHandCalculated(t *testing.T) {
	data := fixtureData()
	res, err := Analyze(data, tolFixture, MethodAuto)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.DataUsage.InteractionPooled {
		t.Fatalf("interaction should not be pooled, reason=%q", res.DataUsage.PoolReason)
	}
	want := map[string]struct{ ss, df, ms float64 }{
		"part":          {742.5, 9, 82.5},
		"operator":      {60, 2, 30},
		"interaction":   {495, 18, 27.5},
		"repeatability": {60, 60, 1},
		"total":         {1357.5, 89, 0},
	}
	for src, w := range want {
		r := rowBySource(res.ANOVA, src)
		if r.Source == "" {
			t.Fatalf("missing ANOVA row %q", src)
		}
		approxEq(t, src+".SS", r.SS, w.ss, 1e-9)
		approxEq(t, src+".DF", r.DF, w.df, 1e-12)
		if src != "total" {
			approxEq(t, src+".MS", *r.MS, w.ms, 1e-9)
		}
	}
	ir := rowBySource(res.ANOVA, "interaction")
	approxEq(t, "interaction.F", *ir.F, 27.5, 1e-9)
	if *ir.P > 1e-20 {
		t.Fatalf("interaction p = %.3e, want ~ 0", *ir.P)
	}
	if !res.SSCheck.WithinTol {
		t.Fatalf("SS partition rel error %.3e", res.SSCheck.RelError)
	}

	comp := func(n string) VarComp {
		for _, c := range res.VarComps {
			if c.Name == n {
				return c
			}
		}
		t.Fatalf("missing component %q", n)
		return VarComp{}
	}
	approxEq(t, "part comp", comp("part").Estimate, 55.0/9, 1e-9)
	approxEq(t, "operator comp", comp("operator").Estimate, 2.5/30, 1e-10)
	approxEq(t, "interaction comp", comp("interaction").Estimate, 26.5/3, 1e-9)
	approxEq(t, "repeat comp", comp("repeatability").Estimate, 1, 1e-12)

	s2TV := 55.0/9 + 2.5/30 + 26.5/3 + 1
	s2GRR := 2.5/30 + 1
	approxEq(t, "TV sd", res.Metrics.TotalVariation.SD, math.Sqrt(s2TV), 1e-9)
	approxEq(t, "pct GRR of total", res.Metrics.PctGRROfTotal,
		math.Sqrt(s2GRR)/math.Sqrt(s2TV)*100, 1e-9)
	approxEq(t, "pct GRR of tolerance", res.Metrics.PctGRROfTolerance,
		6*math.Sqrt(s2GRR)/tolFixture*100, 1e-9)
	ndcWant := int(math.Floor(1.41*math.Sqrt(55.0/9)/math.Sqrt(s2GRR) + 1e-9))
	if res.Metrics.NDC != ndcWant {
		t.Fatalf("NDC = %d, want %d", res.Metrics.NDC, ndcWant)
	}
}

func TestFixtureDropRouteMatchesBalanced(t *testing.T) {
	res, err := Analyze(fixtureData(), tolFixture, MethodBalancedDrop)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !res.SSCheck.WithinTol {
		t.Fatalf("SS partition rel error %.3e", res.SSCheck.RelError)
	}
	approxEq(t, "SSP", rowBySource(res.ANOVA, "part").SS, 742.5, 1e-9)
	approxEq(t, "SST", rowBySource(res.ANOVA, "total").SS, 1357.5, 1e-9)
	if res.DataUsage.UsedReadings != 90 {
		t.Fatalf("used = %d, want 90", res.DataUsage.UsedReadings)
	}
}

// randomBalanced generates a balanced p*o*n dataset from the full random
// model (with interactions).
func randomBalanced(rng *rand.Rand, p, o, n int) []Measurement {
	pa := make([]float64, p)
	op := make([]float64, o)
	in := make([][]float64, p)
	for i := range pa {
		pa[i] = rng.NormFloat64()
	}
	for j := range op {
		op[j] = rng.NormFloat64()
	}
	for i := 0; i < p; i++ {
		in[i] = make([]float64, o)
		for j := range in[i] {
			in[i][j] = rng.NormFloat64() * 0.5
		}
	}
	var out []Measurement
	for i := 0; i < p; i++ {
		for j := 0; j < o; j++ {
			for k := 0; k < n; k++ {
				out = append(out, Measurement{
					PartID: partLabel(i), OperatorID: opLabel(j), Trial: k + 1,
					Value: pa[i] + op[j] + in[i][j] + rng.NormFloat64(),
				})
			}
		}
	}
	return out
}

// TestBalancedRoutesAgree: on balanced data the projection engine must
// reproduce the classical balanced ANOVA exactly (SS, df, MS, components).
func TestBalancedRoutesAgree(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for iter := 0; iter < 50; iter++ {
		p, o := 3+iter%6, 2+iter%3
		data := randomBalanced(rng, p, o, 2+iter%3)
		lay, ok := detectBalanced(data)
		if !ok {
			t.Fatal("expected balanced")
		}
		bal := balancedANOVA(lay, false)
		d := buildDesign(data)
		f := d.fit(false)
		for src := range map[string]bool{
			"part": true, "operator": true, "interaction": true,
			"repeatability": true, "total": true,
		} {
			a := rowBySource(bal.rows, src)
			b := rowBySource(f.rows, src)
			approxEq(t, "SS "+src, a.SS, b.SS, 1e-7*math.Max(1, math.Abs(a.SS)))
			approxEq(t, "DF "+src, a.DF, b.DF, 1e-9)
			if a.MS != nil {
				approxEq(t, "MS "+src, *a.MS, *b.MS, 1e-8*math.Max(1, *a.MS))
			}
		}
		comps := fitComponents(f, false)
		nameMap := map[string]string{
			"part": "part", "operator": "operator",
			"interaction": "interact", "repeatability": "repeat",
		}
		for un, bn := range nameMap {
			got := comps[un].value
			want := bal.rawComps[bn]
			approxEq(t, "comp "+un, got, want, 1e-7*math.Max(1, math.Abs(want)))
		}
	}
}

func TestBalancedPooledRoutesAgree(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for iter := 0; iter < 20; iter++ {
		data := randomBalanced(rng, 4, 3, 3)
		lay, _ := detectBalanced(data)
		bal := balancedANOVA(lay, true)
		f := buildDesign(data).fit(true)
		for _, src := range []string{"part", "operator", "repeatability", "total"} {
			a := rowBySource(bal.rows, src)
			b := rowBySource(f.rows, src)
			approxEq(t, "pooled SS "+src, a.SS, b.SS, 1e-7*math.Max(1, math.Abs(a.SS)))
			approxEq(t, "pooled DF "+src, a.DF, b.DF, 0)
		}
	}
}

func TestInteractionPoolingTriggers(t *testing.T) {
	// Pure-noise, weak-signal data: interaction F sits in the tail with
	// p > 0.25 frequently. Try seeds until one triggers, then check the
	// pooled table equals the additive refit.
	for seed := int64(0); seed < 300; seed++ {
		rng := rand.New(rand.NewSource(seed))
		var data []Measurement
		for i := 0; i < 4; i++ {
			for j := 0; j < 3; j++ {
				for k := 0; k < 3; k++ {
					data = append(data, Measurement{
						PartID: partLabel(i), OperatorID: opLabel(j),
						Trial: k + 1,
						Value: rng.NormFloat64()*3 + rng.NormFloat64()*0.05,
					})
				}
			}
		}
		res, err := Analyze(data, 5, MethodAuto)
		if err != nil {
			t.Fatal(err)
		}
		if !res.DataUsage.InteractionPooled {
			continue
		}
		if rowBySource(res.ANOVA, "interaction").Source != "" {
			t.Fatalf("pooled table must not contain an interaction row")
		}
		// repeat df = p*o*(n-1) + (p-1)(o-1) = 24 + 6 = 30.
		rr := rowBySource(res.ANOVA, "repeatability")
		approxEq(t, "pooled repeat df", rr.DF, 30, 1e-9)
		if !res.SSCheck.WithinTol {
			t.Fatalf("SS partition rel error %.3e", res.SSCheck.RelError)
		}
		// Same via drop route.
		res2, err := Analyze(data, 5, MethodBalancedDrop)
		if err != nil {
			t.Fatal(err)
		}
		if !res2.DataUsage.InteractionPooled {
			t.Fatalf("drop route must pool identically")
		}
		approxEq(t, "routes repeat SS",
			rowBySource(res.ANOVA, "repeatability").SS,
			rowBySource(res2.ANOVA, "repeatability").SS, 1e-9)
		return
	}
	t.Fatal("no seed produced p>0.25 interaction; RNG assumption broken")
}

// TestUnbalancedSSPartition: component SS must equal SST to 1e-9 for many
// random missing patterns.
func TestUnbalancedSSPartition(t *testing.T) {
	keep := func(data []Measurement, pred func(m Measurement) bool) []Measurement {
		var out []Measurement
		for _, m := range data {
			if pred(m) {
				out = append(out, m)
			}
		}
		return out
	}
	structured := []func([]Measurement) []Measurement{
		// Drop a handful of individual trials.
		func(d []Measurement) []Measurement {
			return keep(d, func(m Measurement) bool {
				return !(m.PartID == "P3" && m.OperatorID == "O1" && m.Trial == 2) &&
					!(m.PartID == "P7" && m.Trial == 1)
			})
		},
		// Drop every O2 reading of one part.
		func(d []Measurement) []Measurement {
			return keep(d, func(m Measurement) bool {
				return !(m.PartID == "P4" && m.OperatorID == "O2")
			})
		},
		// Add a fourth replicate to two cells.
		nil,
	}
	for _, tr := range structured[:2] {
		data := tr(randomBalanced(rand.New(rand.NewSource(1)), 10, 3, 3))
		res, err := Analyze(data, 10, MethodAuto)
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		if !res.SSCheck.WithinTol {
			t.Fatalf("rel error %.3e", res.SSCheck.RelError)
		}
	}
	// Extra replicates.
	base := randomBalanced(rand.New(rand.NewSource(2)), 6, 3, 3)
	base = append(base,
		Measurement{PartID: "P1", OperatorID: "O0", Trial: 4, Value: 0.5},
		Measurement{PartID: "P2", OperatorID: "O1", Trial: 4, Value: -0.5},
		Measurement{PartID: "P2", OperatorID: "O1", Trial: 5, Value: 0.2},
	)
	res, err := Analyze(base, 10, MethodAuto)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !res.SSCheck.WithinTol {
		t.Fatalf("rel error %.3e", res.SSCheck.RelError)
	}
	if res.DataUsage.Balanced {
		t.Fatal("design should be reported unbalanced")
	}
}

func TestBalancedDropKeepsLargestCompleteSet(t *testing.T) {
	data := randomBalanced(rand.New(rand.NewSource(3)), 6, 3, 3)
	// Part P0: remove one O2 trial -> incomplete everywhere (2 vs 3), drop.
	// Part P1: remove ALL O2 readings -> incomplete, drop.
	filtered := make([]Measurement, 0, len(data))
	for _, m := range data {
		if m.PartID == "P1" && m.OperatorID == "O2" {
			continue
		}
		if m.PartID == "P0" && m.OperatorID == "O2" && m.Trial == 3 {
			continue
		}
		filtered = append(filtered, m)
	}
	res, err := Analyze(filtered, 10, MethodBalancedDrop)
	if err != nil {
		t.Fatal(err)
	}
	if res.DataUsage.UsedReadings != 4*9 {
		t.Fatalf("used %d, want 36", res.DataUsage.UsedReadings)
	}
	if len(res.DataUsage.Removed) != 12+2 {
		t.Fatalf("removed %d, want 14", len(res.DataUsage.Removed))
	}
	// Result must equal a direct balanced fit on P2..P5.
	var kept []Measurement
	for _, m := range randomBalanced(rand.New(rand.NewSource(3)), 6, 3, 3) {
		if m.PartID != "P0" && m.PartID != "P1" {
			kept = append(kept, m)
		}
	}
	lay, _ := detectBalanced(kept)
	pooled := res.DataUsage.InteractionPooled
	ref := balancedANOVA(lay, pooled)
	approxEq(t, "SS total", rowBySource(res.ANOVA, "total").SS,
		rowBySource(ref.rows, "total").SS, 1e-8)
}

// ---- Invariant audits from the user's checklist ----

func invariantSnapshot(t *testing.T, res *Result) map[string]float64 {
	t.Helper()
	g := func(s string, f func(Stat) float64) float64 {
		for _, st := range []Stat{res.Metrics.Repeatability, res.Metrics.Reproducibility,
			res.Metrics.GRR, res.Metrics.PartVariation, res.Metrics.TotalVariation} {
			if st.Name == s {
				return f(st)
			}
		}
		t.Fatal("missing stat " + s)
		return 0
	}
	return map[string]float64{
		"pctTotal": res.Metrics.PctGRROfTotal,
		"pctTol":   res.Metrics.PctGRROfTolerance,
		"ndc":      float64(res.Metrics.NDC),
		"grrSD":    g("grr", func(s Stat) float64 { return s.SD }),
		"tvSD":     g("total_variation", func(s Stat) float64 { return s.SD }),
		"evSD":     g("repeatability", func(s Stat) float64 { return s.SD }),
	}
}

func transform(data []Measurement, f func(float64) float64) []Measurement {
	out := make([]Measurement, len(data))
	for i, m := range data {
		m.Value = f(m.Value)
		out[i] = m
	}
	return out
}

func unbalancedSubset(data []Measurement) []Measurement {
	out := make([]Measurement, 0, len(data))
	for i, m := range data {
		if m.PartID == "P3" && m.OperatorID == "O1" && m.Trial == 2 {
			continue
		}
		if i%31 == 0 {
			continue
		}
		out = append(out, m)
	}
	return out
}

func TestInvariantAddConstant(t *testing.T) {
	base := fixtureData()
	for _, data := range [][]Measurement{base, unbalancedSubset(base)} {
		r1, err := Analyze(data, 10, MethodAuto)
		if err != nil {
			t.Fatal(err)
		}
		r2, err := Analyze(transform(data, func(x float64) float64 { return x + 137.5 }), 10, MethodAuto)
		if err != nil {
			t.Fatal(err)
		}
		a, b := invariantSnapshot(t, r1), invariantSnapshot(t, r2)
		for k := range a {
			approxEq(t, "shift "+k, a[k], b[k], 1e-7*math.Max(1, math.Abs(a[k])))
		}
		for i := range r1.ANOVA {
			approxEq(t, "shift SS "+r1.ANOVA[i].Source,
				r1.ANOVA[i].SS, r2.ANOVA[i].SS, 1e-7)
		}
	}
}

func TestInvariantScale(t *testing.T) {
	base := fixtureData()
	c := 4.25
	for _, data := range [][]Measurement{base, unbalancedSubset(base)} {
		r1, err := Analyze(data, 10, MethodAuto)
		if err != nil {
			t.Fatal(err)
		}
		scaled := transform(data, func(x float64) float64 { return x * c })
		r2, err := Analyze(scaled, 10*c, MethodAuto)
		if err != nil {
			t.Fatal(err)
		}
		a, b := invariantSnapshot(t, r1), invariantSnapshot(t, r2)
		approxEq(t, "scaled grrSD", b["grrSD"], a["grrSD"]*c, 1e-7)
		approxEq(t, "scaled tvSD", b["tvSD"], a["tvSD"]*c, 1e-7)
		approxEq(t, "scaled evSD", b["evSD"], a["evSD"]*c, 1e-7)
		approxEq(t, "pctTotal stable", b["pctTotal"], a["pctTotal"], 1e-9)
		approxEq(t, "pctTol stable", b["pctTol"], a["pctTol"], 1e-9)
		if int(b["ndc"]) != int(a["ndc"]) {
			t.Fatalf("NDC changed under scaling: %v vs %v", a["ndc"], b["ndc"])
		}
	}
}

func TestInvariantRelabel(t *testing.T) {
	base := fixtureData()
	relabelP := map[string]string{
		"P0": "Z9", "P1": "Q2", "P2": "A0", "P3": "M5", "P4": "B7",
		"P5": "X1", "P6": "C8", "P7": "L3", "P8": "E4", "P9": "N6",
	}
	relabelO := map[string]string{"O0": "Alice", "O1": "Bob", "O2": "Cara"}
	rel := func(data []Measurement) []Measurement {
		return transform2(data, func(m Measurement) Measurement {
			m.PartID = relabelP[m.PartID]
			m.OperatorID = relabelO[m.OperatorID]
			return m
		})
	}
	for _, data := range [][]Measurement{base, unbalancedSubset(base)} {
		r1, err := Analyze(data, 10, MethodAuto)
		if err != nil {
			t.Fatal(err)
		}
		r2, err := Analyze(rel(data), 10, MethodAuto)
		if err != nil {
			t.Fatal(err)
		}
		a, b := invariantSnapshot(t, r1), invariantSnapshot(t, r2)
		for k := range a {
			approxEq(t, "relabel "+k, a[k], b[k], 1e-8*math.Max(1, math.Abs(a[k])))
		}
	}
}

func transform2(data []Measurement, f func(Measurement) Measurement) []Measurement {
	out := make([]Measurement, len(data))
	for i, m := range data {
		out[i] = f(m)
	}
	return out
}

// ---- p-value closed form ----

func TestFSurvClosedForm(t *testing.T) {
	// F(2,2): survival = 1/(1+F)  [derive from beta with x = 2/(2+2F)].
	for _, f := range []float64{0.5, 1, 2.5, 7} {
		approxEq(t, "F(2,2) surv", FSurv(f, 2, 2), 1/(1+f), 1e-12)
	}
	// Symmetry relation: P(F(d1,d2) > 1/f) = 1 - P(F(d2,d1) > f).
	for _, f := range []float64{0.3, 1.7, 4} {
		a := FSurv(1/f, 5, 10)
		b := 1 - FSurv(f, 10, 5)
		approxEq(t, "F symmetry", a, b, 1e-12)
	}
	// Monotonicity.
	prev := 1.0
	for _, f := range []float64{0.01, 0.5, 1, 2, 5, 100} {
		p := FSurv(f, 6, 20)
		if p > prev+1e-15 {
			t.Fatalf("p not decreasing at f=%v", f)
		}
		prev = p
	}
	if FSurv(0, 4, 8) != 1 {
		t.Fatal("FSurv(0) must be 1")
	}
}

// ---- validation ----

func expectField(t *testing.T, err error, field string) {
	t.Helper()
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	if !ve.Has(field) {
		t.Fatalf("expected error on %q; got %v", field, ve.Errors)
	}
}

func TestValidationFieldErrors(t *testing.T) {
	good := fixtureData()

	bad := append([]Measurement(nil), good...)
	bad[5].Value = math.NaN()
	_, err := Analyze(bad, 10, MethodAuto)
	expectField(t, err, "measurements[5].value")

	_, err = Analyze(good, 0, MethodAuto)
	expectField(t, err, "tolerance")
	_, err = Analyze(good, -2, MethodAuto)
	expectField(t, err, "tolerance")
	_, err = Analyze(good, math.Inf(1), MethodAuto)
	expectField(t, err, "tolerance")

	onePart := make([]Measurement, 0)
	for _, m := range good {
		if m.PartID == "P0" || m.PartID == "P1" {
			m.PartID = "P0"
			onePart = append(onePart, m)
		}
	}
	_, err = Analyze(onePart, 10, MethodAuto)
	expectField(t, err, "parts")

	oneOp := make([]Measurement, 0)
	for _, m := range good {
		m.OperatorID = "O0"
		oneOp = append(oneOp, m)
	}
	_, err = Analyze(oneOp, 10, MethodAuto)
	expectField(t, err, "operators")

	dup := append([]Measurement(nil), good...)
	dup = append(dup, dup[10])
	_, err = Analyze(dup, 10, MethodAuto)
	found := false
	if ve, ok := err.(*ValidationError); ok {
		for _, fe := range ve.Errors {
			if len(fe.Field) >= 13 && fe.Field[:13] == "measurements[" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected duplicate index error, got %v", err)
	}

	missing := append([]Measurement(nil), good...)
	missing[3].OperatorID = ""
	_, err = Analyze(missing, 10, MethodAuto)
	expectField(t, err, "measurements[3].operator_id")
}

// TestNegativeVarianceFlag: when operator MS is below interaction MS the
// operator variance component estimate is negative; it must surface as zero
// with clipping flagged (balanced closed form and unbalanced engine alike).
func TestNegativeVarianceFlag(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	found := false
	for seed := int64(0); seed < 500 && !found; seed++ {
		rng.Seed(seed)
		// Big interaction/error, no real operator effect.
		var data []Measurement
		partEff := []float64{-3, -2, -1, 0, 1, 2}
		for i := 0; i < 6; i++ {
			inter := []float64{0, 0, 0}
			for j := range inter {
				inter[j] = rng.NormFloat64() * 1.5
			}
			for j := 0; j < 3; j++ {
				for k := 0; k < 3; k++ {
					data = append(data, Measurement{
						PartID: partLabel(i), OperatorID: opLabel(j),
						Trial: k + 1,
						Value: partEff[i] + inter[j] + rng.NormFloat64(),
					})
				}
			}
		}
		lay, ok := detectBalanced(data)
		if !ok {
			continue
		}
		b := balancedANOVA(lay, false)
		if b.rawComps["operator"] >= 0 {
			continue
		}
		if *rowBySource(b.rows, "interaction").P > 0.25 {
			continue // would pool; skip
		}
		found = true
		res, err := Analyze(data, 50, MethodAuto)
		if err != nil {
			t.Fatal(err)
		}
		if res.DataUsage.InteractionPooled {
			t.Fatal("test setup wrong: pooled")
		}
		var oc VarComp
		for _, c := range res.VarComps {
			if c.Name == "operator" {
				oc = c
			}
		}
		if !oc.Clipped || oc.Estimate != 0 || oc.Raw >= 0 {
			t.Fatalf("operator comp not flagged: %+v", oc)
		}
	}
	if !found {
		t.Skip("no seed produced a negative non-pooled operator component")
	}
}

func TestTinyStudies(t *testing.T) {
	// Single trial per cell: interaction non-testable, pooled automatically.
	var data []Measurement
	for i := 0; i < 3; i++ {
		for j := 0; j < 2; j++ {
			data = append(data, Measurement{
				PartID: partLabel(i), OperatorID: opLabel(j), Trial: 1,
				Value: float64(i)*2 + float64(j) + 0.1*float64(i*j),
			})
		}
	}
	res, err := Analyze(data, 10, MethodAuto)
	if err != nil {
		t.Fatal(err)
	}
	if !res.DataUsage.InteractionPooled {
		t.Fatal("single-replicate design must pool interaction")
	}
	if !res.SSCheck.WithinTol {
		t.Fatalf("SS partition %.3e", res.SSCheck.RelError)
	}
}
