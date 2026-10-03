package stat

import (
	"testing"
)

type rawIn = struct {
	Part     string
	Operator string
	Trial    int
	Value    float64
}

func mk(part, op string, trial int, v float64) rawIn {
	return rawIn{Part: part, Operator: op, Trial: trial, Value: v}
}

func TestFilterBalancedUnchanged(t *testing.T) {
	// Full 3x2x2 design passes through untouched.
	var in []rawIn
	for _, p := range []string{"P1", "P2", "P3"} {
		for _, o := range []string{"A", "B"} {
			for k := 1; k <= 2; k++ {
				in = append(in, mk(p, o, k, 1))
			}
		}
	}
	cc, err := FilterCompleteCases(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(cc.Parts) != 3 || len(cc.Operators) != 2 || cc.R != 2 {
		t.Fatalf("design = %v parts, %v ops, R=%v", cc.Parts, cc.Operators, cc.R)
	}
	if len(cc.Excluded) != 0 || len(cc.Missing) != 0 {
		t.Fatalf("balanced design should not report exclusions: %+v", cc)
	}
	flat := cc.Flatten()
	res, err := ComputeBalancedANOVA(flat, 3, 2, 2, Config{PoolingAlpha: 0.25})
	if err != nil {
		t.Fatal(err)
	}
	// Every reading is 1 -> zero variation.
	if res.SSTotal != 0 {
		t.Errorf("constant data: SSTotal = %v", res.SSTotal)
	}
}

// TestFilterMissingTrials: one cell misses a trial; the common trial set
// shrinks to that size, every reading of the dropped repeat is reported.
func TestFilterMissingTrials(t *testing.T) {
	var in []rawIn
	for _, p := range []string{"P1", "P2"} {
		for _, o := range []string{"A", "B"} {
			in = append(in, mk(p, o, 1, 1), mk(p, o, 2, 1))
		}
	}
	// drop P1/A trial 2; then common trials = {1}, R=1, and three
	// surplus trial-2 readings must be listed as excluded.
	in = append(in[:1], in[2:]...)
	cc, err := FilterCompleteCases(in)
	if err != nil {
		t.Fatal(err)
	}
	if cc.R != 1 || len(cc.Parts) != 2 || len(cc.Operators) != 2 {
		t.Fatalf("got R=%d parts=%v ops=%v", cc.R, cc.Parts, cc.Operators)
	}
	if len(cc.Excluded) != 3 {
		t.Fatalf("excluded = %d, want 3 surplus trial-2 readings: %+v", len(cc.Excluded), cc.Excluded)
	}
	for _, e := range cc.Excluded {
		if e.Trial != 2 {
			t.Errorf("unexpected exclusion %+v", e)
		}
	}
	// Missing slot P1/A trial 2 is recorded.
	foundMissing := false
	for _, m := range cc.Missing {
		if m.Part == "P1" && m.Operator == "A" && m.Trial == 2 {
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Errorf("missing slot not reported: %+v", cc.Missing)
	}
}

// TestFilterPartialPart: a part measured by only some operators is
// dropped entirely, with every one of its readings listed.
func TestFilterPartialPart(t *testing.T) {
	var in []rawIn
	// P1,P2 complete for A and B.
	for _, p := range []string{"P1", "P2"} {
		for _, o := range []string{"A", "B"} {
			in = append(in, mk(p, o, 1, 2), mk(p, o, 2, 3))
		}
	}
	// P3 only measured by A (dropped part).
	in = append(in, mk("P3", "A", 1, 5), mk("P3", "A", 2, 6))
	cc, err := FilterCompleteCases(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(cc.Parts) != 2 {
		t.Fatalf("parts = %v, want P1,P2", cc.Parts)
	}
	for _, p := range cc.Parts {
		if p == "P3" {
			t.Fatal("dropped part retained")
		}
	}
	if len(cc.Excluded) != 2 {
		t.Fatalf("excluded count = %d, want 2: %+v", len(cc.Excluded), cc.Excluded)
	}
	for _, e := range cc.Excluded {
		if e.Part != "P3" {
			t.Errorf("excluded reading from %s, want P3", e.Part)
		}
	}
}

// TestFilterUnequalRepeats: one cell has an extra repeat; the surplus is
// excluded and the balanced analysis runs on the rest.
func TestFilterUnequalRepeats(t *testing.T) {
	var in []rawIn
	v := 0.0
	for _, p := range []string{"P1", "P2", "P3"} {
		for _, o := range []string{"A", "B"} {
			for k := 1; k <= 2; k++ {
				v++
				in = append(in, mk(p, o, k, v))
			}
		}
	}
	in = append(in, mk("P1", "A", 3, 100)) // extra repeat
	cc, err := FilterCompleteCases(in)
	if err != nil {
		t.Fatal(err)
	}
	if cc.R != 2 {
		t.Fatalf("R = %d, want 2", cc.R)
	}
	if len(cc.Excluded) != 1 || cc.Excluded[0].Trial != 3 {
		t.Fatalf("excluded = %+v", cc.Excluded)
	}
	if len(cc.Matrix) != 3 || len(cc.Matrix[0]) != 2 || len(cc.Matrix[0][0]) != 2 {
		t.Fatal("matrix shape wrong")
	}
}

// TestFilterTooFewParts: after dropping, fewer than 2 parts remain.
func TestFilterTooFewParts(t *testing.T) {
	in := []rawIn{
		mk("P1", "A", 1, 1), mk("P1", "B", 1, 2),
		mk("P2", "A", 1, 3), // P2 incomplete
	}
	if _, err := FilterCompleteCases(in); err != ErrNoUsableDesign {
		t.Fatalf("got %v, want ErrNoUsableDesign", err)
	}
}

// TestFilterBalancedMatchesDirectANOVA: on complete balanced input the
// complete-case route yields exactly the standard ANOVA of the same
// numbers.
func TestFilterBalancedMatchesDirectANOVA(t *testing.T) {
	rnd := newRng(123)
	P, O, R := 4, 3, 3
	var in []rawIn
	parts := []string{"x1", "x2", "x3", "x4"}
	ops := []string{"a", "b", "c"}
	direct := make([]float64, 0, P*O*R)
	for i := 0; i < P; i++ {
		for j := 0; j < O; j++ {
			for k := 1; k <= R; k++ {
				x := rnd.Normal()*4 + float64(i*2) - float64(j)
				in = append(in, mk(parts[i], ops[j], k, x))
				direct = append(direct, x)
			}
		}
	}
	cc, err := FilterCompleteCases(in)
	if err != nil {
		t.Fatal(err)
	}
	r1, err := ComputeBalancedANOVA(cc.Flatten(), P, O, R, Config{PoolingAlpha: 0.25})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := ComputeBalancedANOVA(direct, P, O, R, Config{PoolingAlpha: 0.25})
	if err != nil {
		t.Fatal(err)
	}
	if !approxEq(r1.SSTotal, r2.SSTotal, 1e-12) ||
		!approxEq(r1.SSPart, r2.SSPart, 1e-12) ||
		!approxEq(r1.SSOp, r2.SSOp, 1e-12) ||
		!approxEq(r1.SSInter, r2.SSInter, 1e-12) ||
		!approxEq(r1.SSE, r2.SSE, 1e-12) {
		t.Fatal("complete-case route differs from direct balanced ANOVA")
	}
	if r1.Pooled != r2.Pooled || r1.NDC != r2.NDC {
		t.Fatal("pooling/NDC differs between equivalent routes")
	}
}
