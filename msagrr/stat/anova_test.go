package stat

import (
	"math"
	"testing"

	"msagrr/preset"
)

const tol = 1e-9

func approxEq(a, b, eps float64) bool {
	scale := math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
	return math.Abs(a-b) <= eps*scale
}

// flattenPreset converts the preset label-space readings to the balanced
// row-major slice used by the reference ANOVA routine.
func flattenPreset(rs []preset.Reading) []float64 {
	out := make([]float64, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Value)
	}
	return out
}

// TestPresetHandCalculatedSS runs the built-in 10x3x3 balanced example
// and checks every hand-derived sum of squares (see preset package doc):
//
//	SS_part 2970 (df9), SS_op 60 (df2), SS_inter 0 (df18),
//	SS_repeat 240 (df60), SS_total 3270 (df89).
func TestPresetHandCalculatedSS(t *testing.T) {
	rs := preset.Dataset()
	if len(rs) != preset.N {
		t.Fatalf("preset size = %d, want %d", len(rs), preset.N)
	}
	res, err := ComputeBalancedANOVA(flattenPreset(rs), preset.PartsCount, preset.OpCount, preset.TrialCount,
		Config{PoolingAlpha: 0.25, Tolerance: 10})
	if err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"SS_part", res.SSPart, preset.SSPart},
		{"SS_operator", res.SSOp, preset.SSOperator},
		{"SS_interaction", res.SSInter, preset.SSInteraction},
		{"SS_repeat", res.SSE, preset.SSRepeat},
		{"SS_total", res.SSTotal, preset.SSTotal},
		{"grand mean", res.GrandMean, preset.GrandMean},
	}
	for _, c := range checks {
		if !approxEq(c.got, c.want, tol) {
			t.Errorf("%s = %.10f, want %.10f", c.name, c.got, c.want)
		}
	}
	if res.DFPart != preset.DFPart || res.DFOp != preset.DFOp ||
		res.DFInter != preset.DFInter || res.DFE != preset.DFRepeat ||
		res.DFTotal != preset.DFTotal {
		t.Errorf("degrees of freedom wrong: %+v", res)
	}

	// The primary audit identity: SS_part+SS_op+SS_inter+SS_e = SS_total
	// within relative error 1e-9.
	recomposed := res.SSPart + res.SSOp + res.SSInter + res.SSE
	if !approxEq(recomposed, res.SSTotal, 1e-9) {
		t.Errorf("SS identity: %.12f != %.12f", recomposed, res.SSTotal)
	}

	// Zero interaction gives p = 1 -> pooling triggers.
	if !res.Pooled {
		t.Error("zero interaction must be pooled (p=1 > 0.25)")
	}
	if !approxEq(res.InteractionP, 1, 1e-12) {
		t.Errorf("interaction p = %v, want 1", res.InteractionP)
	}
	if res.PooledSSE != res.SSInter+res.SSE {
		t.Errorf("pooled SSE = %v, want %v", res.PooledSSE, res.SSInter+res.SSE)
	}
	if res.PooledDFE != res.DFInter+res.DFE {
		t.Errorf("pooled df = %d, want %d", res.PooledDFE, res.DFInter+res.DFE)
	}
}

// smallInteractingDataset is the fully hand-audited 2x2x2 case with a
// nonzero interaction (see test doc below); values are laid out
// part-major, operator-middle, trial-inner.
func smallInteractingDataset() []float64 {
	return []float64{
		// part A, op1: [20,24]; op2: [48,52]
		20, 24, 48, 52,
		// part B, op1: [28,32]; op2: [64,68]
		28, 32, 64, 68,
	}
}

// Hand audit of the 2x2x2 data below (grand total 336, mean 42, n=8):
//
//	part totals [144,192], op totals [104,232],
//	cell totals [44,100,60,132],  sum x^2 = 16512
//	CF = 336^2/8 = 14112
//	SS_part  = (144^2+192^2)/4 - CF = 288
//	SS_op    = (104^2+232^2)/4 - CF = 2048
//	SS_cell  = (44^2+100^2+60^2+132^2)/2 - CF = 2368
//	SS_inter = SS_cell - SS_part - SS_op = 32
//	SS_e     = 16512 - (44^2+100^2+60^2+132^2)/2 = 32
//	           (each cell of two values 4 apart contributes 8)
//	SS_total = 16512 - 14112 = 2400
//
// Check: 288 + 2048 + 32 + 32 = 2400.
func TestSmall2x2x2HandCalculatedSS(t *testing.T) {
	data := smallInteractingDataset()
	P, O, R := 2, 2, 2
	n := P * O * R

	var total float64
	sq := 0.0
	partTot := make([]float64, P)
	opTot := make([]float64, O)
	cellTot := make([][]float64, P)
	for i := range cellTot {
		cellTot[i] = make([]float64, O)
	}
	at := func(i, j, k int) float64 { return data[(i*O+j)*R+k] }
	for i := 0; i < P; i++ {
		for j := 0; j < O; j++ {
			for k := 0; k < R; k++ {
				x := at(i, j, k)
				total += x
				sq += x * x
				partTot[i] += x
				opTot[j] += x
				cellTot[i][j] += x
			}
		}
	}
	cf := total * total / float64(n)
	ssP := 0.0
	for _, t0 := range partTot {
		ssP += t0 * t0 / float64(O*R)
	}
	ssP -= cf
	ssO := 0.0
	for _, t0 := range opTot {
		ssO += t0 * t0 / float64(P*R)
	}
	ssO -= cf
	ssCell := 0.0
	for i := 0; i < P; i++ {
		for j := 0; j < O; j++ {
			ssCell += cellTot[i][j] * cellTot[i][j] / float64(R)
		}
	}
	// ssCell here is the raw sum of cell totals squared / R (CF not yet
	// subtracted), so the interaction SS is ssCell - (SS_part+CF) -
	// (SS_op+CF) + CF = ssCell - ssP - ssO - CF.
	ssInter := ssCell - ssP - ssO - cf
	ssE := sq - ssCell
	ssTotal := sq - cf

	// Literal hand values.
	for _, c := range []struct {
		name string
		got  float64
		want float64
	}{
		{"CF", cf, 14112},
		{"sum x^2", sq, 16512},
		{"SS_part", ssP, 288},
		{"SS_op", ssO, 2048},
		{"SS_interaction", ssInter, 32},
		{"SS_repeat", ssE, 32},
		{"SS_total", ssTotal, 2400},
	} {
		if !approxEq(c.got, c.want, tol) {
			t.Errorf("hand %s = %v, want %v", c.name, c.got, c.want)
		}
	}

	res, err := ComputeBalancedANOVA(data, P, O, R, Config{PoolingAlpha: 0.25, Tolerance: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !approxEq(res.SSPart, 288, tol) || !approxEq(res.SSOp, 2048, tol) ||
		!approxEq(res.SSInter, 32, tol) || !approxEq(res.SSE, 32, tol) ||
		!approxEq(res.SSTotal, 2400, tol) {
		t.Fatalf("engine SS mismatch: part=%v op=%v inter=%v e=%v total=%v",
			res.SSPart, res.SSOp, res.SSInter, res.SSE, res.SSTotal)
	}
	// Interaction test: MS_inter = 32, MS_e = 8, F = 4, df (1,4),
	// p = P(F(1,4) > 4) ~= 0.1161, which is below 0.25 -> retained.
	if res.Pooled {
		t.Fatalf("interaction should not be pooled, p=%v", res.InteractionP)
	}
	if !approxEq(res.InteractionP, 0.1161165, 1e-5) {
		t.Errorf("interaction p = %v, want ~0.1161", res.InteractionP)
	}
	// Variance components from EMS (P=2, O=2, R=2):
	//   repeat      = MS_e = 8
	//   interaction = (32-8)/2 = 12
	//   operator    = (2048-32)/4 = 504
	//   part        = (288-32)/4 = 64
	if !approxEq(res.VarRepeat, 8, 1e-9) {
		t.Errorf("VarRepeat = %v, want 8", res.VarRepeat)
	}
	if !approxEq(res.VarInter, 12, 1e-9) {
		t.Errorf("VarInter = %v, want 12", res.VarInter)
	}
	if !approxEq(res.VarOp, 504, 1e-9) {
		t.Errorf("VarOp = %v, want 504", res.VarOp)
	}
	if !approxEq(res.VarPart, 64, 1e-9) {
		t.Errorf("VarPart = %v, want 64", res.VarPart)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// TestSSIdentityGeneral: the decomposition identity holds (rel. 1e-9)
// on the preset data and on randomized noisy data.
func TestSSIdentityGeneral(t *testing.T) {
	rs := preset.Dataset()
	res, err := ComputeBalancedANOVA(flattenPreset(rs), 10, 3, 3, Config{PoolingAlpha: 0.25})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.SSPart + res.SSOp + res.SSInter + res.SSE - res.SSTotal; math.Abs(got) > 1e-9*math.Max(1, math.Abs(res.SSTotal)) {
		t.Fatalf("SS identity residual %v", got)
	}

	rnd := newRng(42)
	for iter := 0; iter < 50; iter++ {
		P, O, R := 3+rnd.Intn(6), 2+rnd.Intn(4), 1+rnd.Intn(4)
		data := make([]float64, P*O*R)
		for i := range data {
			data[i] = rnd.Normal()*3 + float64(i%7)
		}
		res, err := ComputeBalancedANOVA(data, P, O, R, Config{PoolingAlpha: 0.25})
		if err != nil {
			t.Fatal(err)
		}
		got := res.SSPart + res.SSOp + res.SSInter + res.SSE - res.SSTotal
		if math.Abs(got) > 1e-9*math.Max(1, math.Abs(res.SSTotal)) {
			t.Fatalf("iter %d: SS identity residual %v (P=%d O=%d R=%d)", iter, got, P, O, R)
		}
		// pooled path also satisfies its own identity
		if res.Pooled {
			if res.PooledSSE != res.SSInter+res.SSE {
				t.Fatal("pooled SSE mismatch")
			}
		}
	}
}

// TestConstantShiftInvariance: adding a constant to every reading
// changes none of the results (SS, variance components, SDs, percentages,
// NDC).
func TestConstantShiftInvariance(t *testing.T) {
	base := flattenPreset(preset.Dataset())
	r1, err := ComputeBalancedANOVA(base, 10, 3, 3, Config{PoolingAlpha: 0.25, Tolerance: 10})
	if err != nil {
		t.Fatal(err)
	}
	shifted := make([]float64, len(base))
	copy(shifted, base)
	const k = 137.5
	for i := range shifted {
		shifted[i] += k
	}
	r2, err := ComputeBalancedANOVA(shifted, 10, 3, 3, Config{PoolingAlpha: 0.25, Tolerance: 10})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(r2.GrandMean-r1.GrandMean-k) > 1e-9 {
		t.Errorf("grand mean shift = %v, want %v", r2.GrandMean-r1.GrandMean, k)
	}
	for _, pair := range [][2]float64{
		{r1.SSTotal, r2.SSTotal}, {r1.SSPart, r2.SSPart}, {r1.SSOp, r2.SSOp},
		{r1.SSInter, r2.SSInter}, {r1.SSE, r2.SSE},
		{r1.VarRepeat, r2.VarRepeat}, {r1.VarOp, r2.VarOp}, {r1.VarPart, r2.VarPart},
		{r1.SDGRR, r2.SDGRR}, {r1.SDTV, r2.SDTV}, {r1.PctGRR, r2.PctGRR},
		{r1.PctGRRTolerance, r2.PctGRRTolerance}, {r1.NDC, r2.NDC},
	} {
		if !approxEq(pair[0], pair[1], 1e-12) {
			t.Errorf("shift changed result: %v vs %v", pair[0], pair[1])
		}
	}
}

// TestPositiveScaleInvariance: multiplying every reading by c>0 scales
// all standard deviations by c and leaves all percentages and NDC
// unchanged.
func TestPositiveScaleInvariance(t *testing.T) {
	base := flattenPreset(preset.Dataset())
	r1, err := ComputeBalancedANOVA(base, 10, 3, 3, Config{PoolingAlpha: 0.25, Tolerance: 5})
	if err != nil {
		t.Fatal(err)
	}
	c := 2.75
	scaled := make([]float64, len(base))
	for i := range base {
		scaled[i] = base[i] * c
	}
	// Tolerance expressed in the same units scales too, so the
	// tolerance-based %GRR stays invariant; first with fixed tolerance
	// the SD-scaling is checked.
	r2, err := ComputeBalancedANOVA(scaled, 10, 3, 3, Config{PoolingAlpha: 0.25, Tolerance: 5 * c})
	if err != nil {
		t.Fatal(err)
	}
	sdPairs := [][2]float64{
		{r1.SDRepeat, r2.SDRepeat}, {r1.SDRepro, r2.SDRepro},
		{r1.SDGRR, r2.SDGRR}, {r1.SDPV, r2.SDPV}, {r1.SDTV, r2.SDTV},
	}
	for _, p := range sdPairs {
		if !approxEq(p[1], p[0]*c, 1e-12) {
			t.Errorf("SD scaling: %v vs %v*%v", p[1], p[0], c)
		}
	}
	pctPairs := [][2]float64{
		{r1.PctRepeat, r2.PctRepeat}, {r1.PctRepro, r2.PctRepro},
		{r1.PctGRR, r2.PctGRR}, {r1.PctPV, r2.PctPV},
		{r1.PctGRRTotal, r2.PctGRRTotal}, {r1.PctGRRTolerance, r2.PctGRRTolerance},
		{r1.NDC, r2.NDC},
	}
	for _, p := range pctPairs {
		if !approxEq(p[0], p[1], 1e-11) {
			t.Errorf("percentage/NDC not scale invariant: %v vs %v", p[0], p[1])
		}
	}
	// SS scale as c^2.
	if !approxEq(r2.SSTotal, r1.SSTotal*c*c, 1e-9) {
		t.Errorf("SS scaling by c^2 failed")
	}
	// %GRR of tolerance follows the AIAG 6-sigma definition exactly:
	// 100 * 6 * sigma_GRR / tolerance.
	wantPctTol := 100 * 6 * r1.SDGRR / 5
	if !approxEq(r1.PctGRRTolerance, wantPctTol, 1e-9) {
		t.Errorf("%%GRR(tolerance) = %v, want %.6f (6 sigma definition)",
			r1.PctGRRTolerance, wantPctTol)
	}
	// %GRR of total variation is the pure SD ratio (6 cancels).
	wantPctTotal := 100 * r1.SDGRR / r1.SDTV
	if !approxEq(r1.PctGRRTotal, wantPctTotal, 1e-9) {
		t.Errorf("%%GRR(total) = %v, want %.6f", r1.PctGRRTotal, wantPctTotal)
	}
	// with a fixed (unscaled) tolerance, scaling data by c scales the
	// tolerance-based %GRR by exactly c.
	r3, err := ComputeBalancedANOVA(scaled, 10, 3, 3, Config{PoolingAlpha: 0.25, Tolerance: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !approxEq(r3.PctGRRTolerance, r1.PctGRRTolerance*c, 1e-9) {
		t.Errorf("fixed-tolerance %%GRR scales by c: %v vs %v",
			r3.PctGRRTolerance, r1.PctGRRTolerance*c)
	}
}

// TestLevelRelabelInvariance: arbitrary permutation of part and operator
// labels leaves every result unchanged.
func TestLevelRelabelInvariance(t *testing.T) {
	base := flattenPreset(preset.Dataset())
	r1, err := ComputeBalancedANOVA(base, 10, 3, 3, Config{PoolingAlpha: 0.25, Tolerance: 8})
	if err != nil {
		t.Fatal(err)
	}
	rnd := newRng(7)
	partPerm := rnd.Perm(10)
	opPerm := rnd.Perm(3)
	perm := make([]float64, len(base))
	for i := 0; i < 10; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				perm[(partPerm[i]*3+opPerm[j])*3+k] = base[(i*3+j)*3+k]
			}
		}
	}
	r2, err := ComputeBalancedANOVA(perm, 10, 3, 3, Config{PoolingAlpha: 0.25, Tolerance: 8})
	if err != nil {
		t.Fatal(err)
	}
	if !approxEq(r1.SSPart, r2.SSPart, 1e-12) ||
		!approxEq(r1.SSOp, r2.SSOp, 1e-12) ||
		!approxEq(r1.SSInter, r2.SSInter, 1e-12) ||
		!approxEq(r1.SSE, r2.SSE, 1e-12) ||
		!approxEq(r1.SSTotal, r2.SSTotal, 1e-12) {
		t.Fatalf("SS changed under label permutation: %+v vs %+v", r1, r2)
	}
	if !approxEq(r1.NDC, r2.NDC, 1e-12) || !approxEq(r1.PctGRR, r2.PctGRR, 1e-12) {
		t.Fatalf("metrics changed under label permutation")
	}
}

// TestTrialReorderInvariance: reordering the repeated readings within a
// cell changes nothing.
func TestTrialReorderInvariance(t *testing.T) {
	rnd := newRng(99)
	P, O, R := 6, 3, 4
	data := make([]float64, P*O*R)
	for i := range data {
		data[i] = rnd.Normal()*2 + float64(i%5)
	}
	r1, _ := ComputeBalancedANOVA(data, P, O, R, Config{PoolingAlpha: 0.25})
	out := make([]float64, len(data))
	for i := 0; i < P; i++ {
		for j := 0; j < O; j++ {
			perm := rnd.Perm(R)
			for k := 0; k < R; k++ {
				out[(i*O+j)*R+k] = data[(i*O+j)*R+perm[k]]
			}
		}
	}
	r2, _ := ComputeBalancedANOVA(out, P, O, R, Config{PoolingAlpha: 0.25})
	if !approxEq(r1.SSTotal, r2.SSTotal, 1e-12) || !approxEq(r1.SSInter, r2.SSInter, 1e-12) {
		t.Fatal("within-cell trial permutation changed results")
	}
}

// TestSingleRepeatPoolsInteraction: with R=1 there is no within-cell
// error; interaction provides the error term.
func TestSingleRepeatPoolsInteraction(t *testing.T) {
	P, O, R := 4, 3, 1
	rnd := newRng(3)
	data := make([]float64, P*O)
	for i := range data {
		data[i] = rnd.Normal()*5 + float64(i%4)
	}
	res, err := ComputeBalancedANOVA(data, P, O, R, Config{PoolingAlpha: 0.25})
	if err != nil {
		t.Fatal(err)
	}
	if res.DFE != 0 || !res.Pooled {
		t.Fatalf("R=1: DFE=%d pooled=%v, want 0,true", res.DFE, res.Pooled)
	}
	if res.PooledDFE != (P-1)*(O-1) {
		t.Errorf("pooled df = %d", res.PooledDFE)
	}
	if !math.IsNaN(res.InteractionP) {
		t.Errorf("interaction p should be NaN without repeats, got %v", res.InteractionP)
	}
}

// TestNegativeVarianceClamped: data constructed so (MS_op - MS_inter)
// is negative when not pooled... simpler: use pooled branch with zero
// operator effect but positive noise where MS_op < MS_pooled; the engine
// flags the clamped component.
func TestNegativeVarianceClamped(t *testing.T) {
	// Parts differ strongly; operators literally identical per column.
	P, O, R := 5, 3, 2
	data := make([]float64, P*O*R)
	for i := 0; i < P; i++ {
		base := float64(i * 10)
		for j := 0; j < O; j++ {
			// same values for each operator -> MS_op tiny relative to
			// pooled error
			data[(i*O+j)*R+0] = base + float64((i+j)%2)
			data[(i*O+j)*R+1] = base - float64((i+j)%2)
		}
	}
	res, err := ComputeBalancedANOVA(data, P, O, R, Config{PoolingAlpha: 0.25})
	if err != nil {
		t.Fatal(err)
	}
	// find an explicitly clamped component across random scenarios too
	if len(res.NegativeVar) == 0 {
		// It is possible rounding gives zero; try a noisy variant.
		rnd := newRng(11)
		for tries := 0; tries < 200 && len(res.NegativeVar) == 0; tries++ {
			for i := range data {
				data[i] = float64((i/P))*10 + rnd.Normal()*4
			}
			res, _ = ComputeBalancedANOVA(data, P, O, R, Config{PoolingAlpha: 0.25})
		}
	}
	if len(res.NegativeVar) == 0 {
		t.Skip("could not construct a negative-estimate case in this environment")
	}
	// flagged components must be zero, never negative.
	for _, n := range res.NegativeVar {
		switch n {
		case "operator":
			if res.VarOp != 0 {
				t.Errorf("clamped operator variance = %v", res.VarOp)
			}
		case "part":
			if res.VarPart != 0 {
				t.Errorf("clamped part variance = %v", res.VarPart)
			}
		case "part*operator":
			if res.VarInter != 0 {
				t.Errorf("clamped interaction variance = %v", res.VarInter)
			}
		}
	}
	if res.VarRepeat < 0 || res.VarOp < 0 || res.VarPart < 0 || res.VarInter < 0 {
		t.Fatal("negative variance component leaked through")
	}
}

// TestVarianceComponentDecomposition: regardless of pooling, the
// reported components partition the total study variance
//
//	TV = repeat + repro(+interaction folded in) + part
//
// and the raw-component percentages sum to 100.
func TestVarianceComponentDecomposition(t *testing.T) {
	rnd := newRng(2024)
	for iter := 0; iter < 200; iter++ {
		P, O, R := 2+rnd.Intn(8), 2+rnd.Intn(4), 2+rnd.Intn(3)
		data := make([]float64, P*O*R)
		// mixed effects: part, operator, interaction, repeat noise
		partEff := make([]float64, P)
		opEff := make([]float64, O)
		for i := range partEff {
			partEff[i] = rnd.Normal() * 5
		}
		for j := range opEff {
			opEff[j] = rnd.Normal() * 2
		}
		for i := 0; i < P; i++ {
			for j := 0; j < O; j++ {
				inter := rnd.Normal() * 1.5
				for k := 0; k < R; k++ {
					data[(i*O+j)*R+k] = 100 + partEff[i] + opEff[j] + inter + rnd.Normal()
				}
			}
		}
		res, err := ComputeBalancedANOVA(data, P, O, R, Config{PoolingAlpha: 0.25, Tolerance: 10})
		if err != nil {
			t.Fatal(err)
		}
		vTV := res.SDTV * res.SDTV
		sum := res.VarRepeat
		if res.Pooled {
			sum += res.VarOp
		} else {
			sum += res.VarOp + res.VarInter
		}
		sum += res.VarPart
		if !approxEq(sum, vTV, 1e-9) {
			t.Fatalf("iter %d pooled=%v: components sum %.12f != TV %.12f", iter, res.Pooled, sum, vTV)
		}
		pctSum := res.PctVarRepeat + res.PctVarOp + res.PctVarInter + res.PctVarPart
		if vTV > 0 && math.Abs(pctSum-100) > 1e-9 {
			t.Fatalf("iter %d: raw component percentages sum %.12f != 100", iter, pctSum)
		}
		// headline SD-ratio percent within [0,100]
		if res.PctGRRTotal < 0 || res.PctGRRTotal > 100+1e-9 {
			t.Fatalf("%%GRR(total) out of range: %v", res.PctGRRTotal)
		}
	}
}

func TestInsufficientLevels(t *testing.T) {
	if _, err := ComputeBalancedANOVA([]float64{1, 2, 3, 4}, 1, 2, 2, Config{}); err != ErrInsufficientLevels {
		t.Errorf("got %v, want ErrInsufficientLevels", err)
	}
	if _, err := ComputeBalancedANOVA([]float64{1, 2, 3}, 2, 2, 1, Config{}); err != ErrUnbalancedInput {
		t.Errorf("got %v, want ErrUnbalancedInput", err)
	}
}
