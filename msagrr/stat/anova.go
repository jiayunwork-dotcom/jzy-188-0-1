package stat

import (
	"math"
	"sort"
)

// Reading is one measured value. Part and Operator are level indices
// (0-based) after the domain labels have been mapped down; Trial is the
// 1-based repeat index within the part-operator cell.
type Reading struct {
	Part     int
	Operator int
	Trial    int
	Value    float64
}

// ANOVAEngineResult holds a fully computed crossed two-factor ANOVA with
// interaction, plus the Gauge R&R quantities derived from it.
type ANOVAEngineResult struct {
	N, P, O, R int // total readings, parts, operators, common repeats/cell

	// Mean of the (possibly filtered) readings.
	GrandMean float64

	// Classical sums of squares / degrees of freedom before any pooling.
	SSTotal float64
	SSPart  float64
	SSOp    float64
	SSInter float64
	SSE     float64
	DFPart  int
	DFOp    int
	DFInter int
	DFE     int
	DFTotal int

	// Rows as emitted in the ANOVA table. Pooled is true when the
	// interaction term was pooled into repeatability (p > threshold).
	Pooled       bool
	InteractionP float64 // NaN when not testable (single repeat)
	PoolingAlpha float64
	Rows         []ANOVARow
	PooledSSE    float64
	PooledDFE    int

	// ANOVA-based variance components (interaction is included in
	// repeatability exactly when Pooled).
	VarPart     float64
	VarOp       float64
	VarInter    float64
	VarRepeat   float64
	NegativeVar []string // names of raw components clamped to zero

	// Each raw component as a percent of the total study variance
	// (TV = repeat + repro[+interaction] + part; rows sum to 100).
	PctVarRepeat float64
	PctVarOp     float64
	PctVarInter  float64
	PctVarPart   float64

	// Gauge R&R standard deviations and percentages.
	SDRepeat float64
	SDRepro  float64
	SDGRR    float64
	SDPV     float64
	SDTV     float64

	// Contribution to total variance (variance ratio), percent.
	PctRepeat float64
	PctRepro  float64
	PctGRR    float64
	PctPV     float64

	// %GRR against total variation and against the tolerance.
	PctGRRTotal     float64
	PctGRRTolerance float64

	NDC float64 // number of distinct categories
}

// ANOVARow is one line of the ANOVA table. F and P are NaN when the
// ratio is not defined (missing denominator mean square).
type ANOVARow struct {
	Source string
	SS     float64
	DF     int
	MS     float64
	F      float64
	P      float64
}

// Config controls the analysis. PoolingAlpha is the interaction p-value
// threshold above which interaction is pooled into repeatability
// (AIAG uses 0.25).
type Config struct {
	PoolingAlpha float64
	Tolerance    float64
}

// kahanSum accumulates values with compensated summation to keep the
// SS identity SS_part+SS_op+SS_inter+SS_e = SS_total within 1e-9 even
// on large data sets.
type kahanSum struct{ s, c float64 }

func (k *kahanSum) add(v float64) {
	y := v - k.c
	t := k.s + y
	k.c = (t - k.s) - y
	k.s = t
}

func sum(xs []float64) float64 {
	var k kahanSum
	for _, x := range xs {
		k.add(x)
	}
	return k.s
}

// ComputeBalancedANOVA runs the classical crossed Gauge R&R ANOVA on a
// balanced design: P parts, O operators, R >= 1 repeated readings per
// cell, n = P*O*R readings. readings[i*O*R + j*R + k] is the k-th
// reading of part i by operator j (k = 0..R-1).
//
// This is the reference implementation: any route on balanced data must
// agree with these numbers.
func ComputeBalancedANOVA(readings []float64, P, O, R int, cfg Config) (*ANOVAEngineResult, error) {
	if P < 2 || O < 2 {
		return nil, ErrInsufficientLevels
	}
	if R < 1 || len(readings) != P*O*R {
		return nil, ErrUnbalancedInput
	}
	if cfg.PoolingAlpha == 0 {
		cfg.PoolingAlpha = 0.25
	}

	res := &ANOVAEngineResult{N: P * O * R, P: P, O: O, R: R, PoolingAlpha: cfg.PoolingAlpha}

	at := func(i, j, k int) float64 { return readings[(i*O+j)*R+k] }

	// Cell, operator and part totals.
	cellTot := make([][]float64, P)
	partTot := make([]float64, P)
	opTot := make([]float64, O)
	var grand kahanSum
	for i := 0; i < P; i++ {
		cellTot[i] = make([]float64, O)
		for j := 0; j < O; j++ {
			var cs kahanSum
			for k := 0; k < R; k++ {
				x := at(i, j, k)
				cs.add(x)
			}
			cellTot[i][j] = cs.s
			partTot[i] += cs.s
			opTot[j] += cs.s
			grand.add(cs.s)
		}
	}
	res.GrandMean = grand.s / float64(res.N)

	n := float64(res.N)

	// Sums of squares using total/cell/part/operator sums.
	var ssP, ssO, ssCell, ssAll kahanSum
	for i := 0; i < P; i++ {
		ssP.add(partTot[i] * partTot[i] / float64(O*R))
	}
	for j := 0; j < O; j++ {
		ssO.add(opTot[j] * opTot[j] / float64(P*R))
	}
	for i := 0; i < P; i++ {
		for j := 0; j < O; j++ {
			ssCell.add(cellTot[i][j] * cellTot[i][j] / float64(R))
		}
	}
	for _, x := range readings {
		ssAll.add(x * x)
	}
	cf := grand.s * grand.s / n
	res.SSPart = ssP.s - cf
	res.SSOp = ssO.s - cf
	res.SSInter = ssCell.s - ssP.s - ssO.s + cf
	res.SSE = ssAll.s - ssCell.s
	res.SSTotal = ssAll.s - cf

	res.DFPart = P - 1
	res.DFOp = O - 1
	res.DFInter = (P - 1) * (O - 1)
	res.DFE = P * O * (R - 1)
	res.DFTotal = res.N - 1

	msP := res.SSPart / float64(res.DFPart)
	msO := res.SSOp / float64(res.DFOp)
	msAB := res.SSInter / float64(res.DFInter)
	var msE float64
	if res.DFE > 0 {
		msE = res.SSE / float64(res.DFE)
	}
	res.InteractionP = math.NaN()

	// F-tests before pooling. Interaction is exactly testable whenever
	// there is at least one repeat (F = MS_AB/MS_e).
	if res.DFE > 0 {
		pAB := FPValue(msAB/msE, float64(res.DFInter), float64(res.DFE))
		res.InteractionP = pAB
		if pAB > cfg.PoolingAlpha {
			res.Pooled = true
		}
	} else {
		// R == 1: there is no within-cell error, so interaction cannot
		// be tested and must provide the error term (pooled).
		res.Pooled = true
	}

	if res.Pooled {
		res.PooledSSE = res.SSInter + res.SSE
		res.PooledDFE = res.DFInter + res.DFE
	} else {
		res.PooledSSE = res.SSE
		res.PooledDFE = res.DFE
	}
	msDenom := res.PooledSSE / float64(res.PooledDFE)

	// ANOVA rows.
	res.Rows = []ANOVARow{
		{Source: "part", SS: res.SSPart, DF: res.DFPart, MS: msP,
			F: safeDiv(msP, msDenom), P: FPValue(safeDiv(msP, msDenom), float64(res.DFPart), float64(res.PooledDFE))},
		{Source: "operator", SS: res.SSOp, DF: res.DFOp, MS: msO,
			F: safeDiv(msO, msDenom), P: FPValue(safeDiv(msO, msDenom), float64(res.DFOp), float64(res.PooledDFE))},
	}
	if res.Pooled {
		res.Rows = append(res.Rows,
			ANOVARow{Source: "part*operator (pooled into repeatability)", SS: res.SSInter, DF: res.DFInter,
				MS: msAB, F: math.NaN(), P: math.NaN()},
			ANOVARow{Source: "repeatability", SS: res.PooledSSE, DF: res.PooledDFE,
				MS: msDenom, F: math.NaN(), P: math.NaN()},
		)
	} else {
		res.Rows = append(res.Rows,
			ANOVARow{Source: "part*operator", SS: res.SSInter, DF: res.DFInter, MS: msAB,
				F: msAB / msE, P: res.InteractionP},
			ANOVARow{Source: "repeatability", SS: res.SSE, DF: res.DFE, MS: msE,
				F: math.NaN(), P: math.NaN()},
		)
	}

	// Variance components from expected mean squares.
	//
	// model: x_ijk = mu + P_i + O_j + (PO)_ij + e_ijk
	//   MS_e      = sigma2_e
	//   MS_AB     = sigma2_e + R*sigma2_PO
	//   MS_O      = sigma2_e + R*sigma2_PO + P*R*sigma2_O
	//   MS_P      = sigma2_e + R*sigma2_PO + O*R*sigma2_P
	var vRepeat, vInter, vOp, vPart float64
	if res.Pooled {
		vRepeat = msDenom // pooled repeatability includes interaction
		vInter = 0
		vOp = clampNonNeg((msO-msDenom)/float64(P*R), &res.NegativeVar, "operator")
		vPart = clampNonNeg((msP-msDenom)/float64(O*R), &res.NegativeVar, "part")
	} else {
		vRepeat = msE // pure repeatability; interaction stays separate
		vInter = clampNonNeg((msAB-msE)/float64(R), &res.NegativeVar, "part*operator")
		vOp = clampNonNeg((msO-msAB)/float64(P*R), &res.NegativeVar, "operator")
		vPart = clampNonNeg((msP-msAB)/float64(O*R), &res.NegativeVar, "part")
	}
	res.VarRepeat = vRepeat
	res.VarInter = vInter
	res.VarOp = vOp
	res.VarPart = vPart

	// Aggregation to repeatability / reproducibility / GRR / PV / TV.
	//
	// AIAG MSA: when interaction is pooled (p > alpha), its variation is
	// part of repeatability. When interaction is retained, it is a
	// gauge-system component assigned to reproducibility, so the total
	// study variance always decomposes without a "missing" term:
	//
	//	TV = repeatability + reproducibility + part variation
	//
	// pooled:     repeat = MS_e(pooled), repro = sigma2_op
	// retained:   repeat = sigma2_e,   repro = sigma2_op + sigma2_PO
	vRepro := vOp
	if !res.Pooled {
		vRepro += vInter
	}
	vGRR := vRepeat + vRepro
	vPV := vPart
	vTV := vGRR + vPV

	res.SDRepeat = math.Sqrt(vRepeat)
	res.SDRepro = math.Sqrt(vRepro)
	res.SDGRR = math.Sqrt(vGRR)
	res.SDPV = math.Sqrt(vPV)
	res.SDTV = math.Sqrt(vTV)

	if vTV > 0 {
		res.PctRepeat = 100 * vRepeat / vTV
		res.PctRepro = 100 * vRepro / vTV
		res.PctGRR = 100 * vGRR / vTV
		res.PctPV = 100 * vPV / vTV
		// AIAG headline %GRR is a standard-deviation ratio.
		res.PctGRRTotal = 100 * res.SDGRR / res.SDTV
		// raw-component percentages that sum to 100
		res.PctVarRepeat = 100 * vRepeat / vTV
		res.PctVarOp = 100 * vOp / vTV
		res.PctVarInter = 100 * vInter / vTV
		res.PctVarPart = 100 * vPart / vTV
	} else {
		res.PctGRRTotal = math.NaN()
	}
	// %GRR against the process/total spread is a pure SD ratio (the
	// 6-sigma factor cancels): 100 * sigma_GRR / sigma_TV.
	//
	// %GRR against tolerance compares the gauge's 6-sigma spread
	// (99.73% interval, AIAG convention used in standard MSA templates):
	//   100 * 6 * sigma_GRR / tolerance.
	if cfg.Tolerance > 0 {
		res.PctGRRTolerance = 100 * 6 * res.SDGRR / cfg.Tolerance
	} else {
		res.PctGRRTolerance = math.NaN()
	}
	if res.SDGRR > 0 {
		res.NDC = math.Floor(1.41 * res.SDPV / res.SDGRR)
	} else {
		res.NDC = math.NaN()
	}
	sort.Strings(res.NegativeVar)
	return res, nil
}

func safeDiv(a, b float64) float64 {
	if b == 0 {
		return math.NaN()
	}
	return a / b
}

func clampNonNeg(v float64, flagged *[]string, name string) float64 {
	if v < 0 {
		*flagged = append(*flagged, name)
		return 0
	}
	return v
}
