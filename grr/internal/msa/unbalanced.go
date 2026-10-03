package msa

import (
	"sort"

	"github.com/automotive/grr/internal/mat"
)

// The unbalanced engine fits a nested sequence of models by orthogonal
// projection (Henderson's Method I):
//
//	M0 = grand mean
//	M1 = M0 + part effects
//	M2 = M1 + operator effects
//	M3 = M2 + part×operator cell effects
//
// For P_k = H(M_k)-H(M_{k-1}) the sequential sums of squares are y'P_k y;
// they partition the corrected total exactly for ANY missingness pattern.
// Expected mean squares are read off from traces
//
//	E(MS_k) = Σ_c [ tr(P_k Z_c Z_c') / df_k ] σ²_c + σ²_e
//
// where Z_c are the raw indicator matrices of part / operator / cell levels,
// so no balance assumption is hidden in the algebra.

// fit is one fitted model (full or interaction-pooled).
type fit struct {
	rows     []ANOVARow // excludes total
	totalSS  float64
	totalDF  float64
	pooled   bool
	partIdx  []string
	opIdx    []string
	hasInter bool
}

type unbDesign struct {
	y             []float64
	partOf, opOf  []int
	p, o          int
	parts, ops    []string
	zP, zO, zCell [][]float64
}

func buildDesign(data []Measurement) *unbDesign {
	partMap, opMap := map[string]int{}, map[string]int{}
	parts, ops := []string{}, []string{}
	for _, m := range data {
		if _, ok := partMap[m.PartID]; !ok {
			partMap[m.PartID] = len(parts)
			parts = append(parts, m.PartID)
		}
		if _, ok := opMap[m.OperatorID]; !ok {
			opMap[m.OperatorID] = len(ops)
			ops = append(ops, m.OperatorID)
		}
	}
	sort.Strings(parts)
	sort.Strings(ops)
	for i, s := range parts {
		partMap[s] = i
	}
	for i, s := range ops {
		opMap[s] = i
	}
	n := len(data)
	p, o := len(parts), len(ops)
	d := &unbDesign{
		y: make([]float64, n), partOf: make([]int, n), opOf: make([]int, n),
		p: p, o: o, parts: parts, ops: ops,
		zP: make([][]float64, p), zO: make([][]float64, o),
	}
	for i, m := range data {
		d.y[i] = m.Value
		d.partOf[i] = partMap[m.PartID]
		d.opOf[i] = opMap[m.OperatorID]
	}
	// Centered part indicators (constant direction belongs to M0).
	for a := 0; a < p; a++ {
		col := make([]float64, n)
		for i := 0; i < n; i++ {
			if d.partOf[i] == a {
				col[i] = 1 - 1/float64(p)
			} else {
				col[i] = -1 / float64(p)
			}
		}
		d.zP[a] = col
	}
	for b := 0; b < o; b++ {
		col := make([]float64, n)
		for i := 0; i < n; i++ {
			if d.opOf[i] == b {
				col[i] = 1 - 1/float64(o)
			} else {
				col[i] = -1 / float64(o)
			}
		}
		d.zO[b] = col
	}
	// RAW cell indicators, one per observed cell (p*o columns max):
	// cell-level interaction deviations are iid across observed
	// part×operator combinations, so the interaction covariance space is the
	// full cell space (rank redundancies removed by the projectors).
	cellCols := make([][]float64, p*o)
	for a := 0; a < p; a++ {
		for b := 0; b < o; b++ {
			col := make([]float64, n)
			present := false
			for i := 0; i < n; i++ {
				if d.partOf[i] == a && d.opOf[i] == b {
					col[i] = 1
					present = true
				}
			}
			if present {
				cellCols[a*o+b] = col
			}
		}
	}
	for _, col := range cellCols {
		if col != nil {
			d.zCell = append(d.zCell, col)
		}
	}
	return d
}

func (d *unbDesign) fit(pooled bool) *fit {
	n := len(d.y)
	m0 := mat.NewHat(mat.StackColumns(onesN(n)))
	m1 := mat.NewHat(mat.StackColumns(append([][]float64{onesN(n)}, d.zP...)...))
	m2cols := append([][]float64{onesN(n)}, d.zP...)
	m2cols = append(m2cols, d.zO...)
	m2 := mat.NewHat(mat.StackColumns(m2cols...))

	var m3 *mat.Hat
	if !pooled {
		m3cols := append([][]float64{onesN(n)}, d.zP...)
		m3cols = append(m3cols, d.zO...)
		m3cols = append(m3cols, d.zCell...)
		m3 = mat.NewHat(mat.StackColumns(m3cols...))
	}

	// Sequential residual sums of squares.
	sse0 := mat.Norm2(mat.ColDiff(m0, mat.StackColumns(d.y)).ColViewVec())
	sse1 := mat.Norm2(mat.ColDiff(m1, mat.StackColumns(d.y)).ColViewVec())
	sse2 := mat.Norm2(mat.ColDiff(m2, mat.StackColumns(d.y)).ColViewVec())

	type line struct {
		name      string
		ss, df    float64
		prev, cur *mat.Hat
	}
	lines := []line{
		{"part", sse0 - sse1, float64(m1.Q.Cols() - m0.Q.Cols()), m0, m1},
		{"operator", sse1 - sse2, float64(m2.Q.Cols() - m1.Q.Cols()), m1, m2},
	}
	var sseR, dfR float64
	if pooled {
		sseR = sse2
		dfR = float64(n - m2.Q.Cols())
	} else {
		sse3 := mat.Norm2(mat.ColDiff(m3, mat.StackColumns(d.y)).ColViewVec())
		lines = append(lines, line{"interaction", sse2 - sse3,
			float64(m3.Q.Cols() - m2.Q.Cols()), m2, m3})
		sseR = sse3
		dfR = float64(n - m3.Q.Cols())
	}

	// EMS coefficient c for one indicator family:
	// c = ||(P_cur - P_prev) Z||²_F / df.
	coef := func(prev, cur *mat.Hat, z [][]float64, df float64) float64 {
		if df <= 0 {
			return 0
		}
		zm := mat.StackColumns(z...)
		pc := cur.ApplyCols(zm)
		pp := prev.ApplyCols(zm)
		s := 0.0
		for j := 0; j < len(z); j++ {
			for i := 0; i < n; i++ {
				dv := pc.At(i, j) - pp.At(i, j)
				s += dv * dv
			}
		}
		return s / df
	}

	msR := sseR / dfR
	f := &fit{
		totalSS: sse0, totalDF: float64(n - 1), pooled: pooled,
		partIdx: d.parts, opIdx: d.ops, hasInter: !pooled,
	}

	ext := make([]extLine, 0, len(lines))
	for _, ln := range lines {
		msv := ln.ss / ln.df
		ext = append(ext, extLine{
			name: ln.name, ss: ln.ss, df: ln.df, ms: msv,
			ems: [4]float64{
				coef(ln.prev, ln.cur, d.zP, ln.df),
				coef(ln.prev, ln.cur, d.zO, ln.df),
				coef(ln.prev, ln.cur, d.zCell, ln.df),
				1,
			},
		})
	}

	comps := solveComponents(ext, msR, pooled)

	for i := range ext {
		e := ext[i]
		row := ANOVARow{
			Source: e.name, SS: e.ss, DF: e.df, MS: ptr(e.ms),
			EMS: []float64{e.ems[0], e.ems[1], e.ems[2], e.ems[3]},
		}
		if vc, ok := comps[e.name]; ok && !vc.estimable {
			row.NonEstimable = true
		}
		attachFTest(&row, e, ext, msR, dfR)
		f.rows = append(f.rows, row)
	}
	f.rows = append(f.rows, ANOVARow{
		Source: "repeatability", SS: sseR, DF: dfR, MS: ptr(msR),
		EMS: []float64{0, 0, 0, 1},
	})
	f.rows = append(f.rows, ANOVARow{Source: "total", SS: f.totalSS, DF: f.totalDF})
	return f
}

func onesN(n int) []float64 {
	o := make([]float64, n)
	for i := range o {
		o[i] = 1
	}
	return o
}

type extLine struct {
	name   string
	ss, df float64
	ms     float64
	ems    [4]float64
}

type vcInfo struct {
	value     float64
	estimable bool
}

// solveComponents converts the EMS equations into variance components.
func solveComponents(ext []extLine, msR float64, pooled bool) map[string]vcInfo {
	get := func(name string) extLine {
		for _, e := range ext {
			if e.name == name {
				return e
			}
		}
		return extLine{}
	}
	out := map[string]vcInfo{
		"repeatability": {value: msR, estimable: true},
	}
	if pooled {
		a := mat.New(2, 2)
		rhs := make([]float64, 2)
		for i, en := range []string{"part", "operator"} {
			e := get(en)
			a.Set(i, 0, e.ems[0])
			a.Set(i, 1, e.ems[1])
			rhs[i] = e.ms - msR
		}
		x, ok := mat.SolveSquare(a, rhs)
		out["part"] = vcInfo{estimable: ok}
		out["operator"] = vcInfo{estimable: ok}
		if ok {
			out["part"] = vcInfo{value: x[0], estimable: true}
			out["operator"] = vcInfo{value: x[1], estimable: true}
		}
		return out
	}
	// Full model: interaction first (only its interaction coefficient nonzero).
	ie := get("interaction")
	iv := vcInfo{estimable: false}
	if ie.ems[2] > 1e-9 {
		iv = vcInfo{value: (ie.ms - msR) / ie.ems[2], estimable: true}
	}
	out["interaction"] = iv
	// Main effects are solved against the RAW interaction estimate; any
	// negative values are clipped only at presentation time.
	a := mat.New(2, 2)
	rhs := make([]float64, 2)
	for i, en := range []string{"part", "operator"} {
		e := get(en)
		a.Set(i, 0, e.ems[0])
		a.Set(i, 1, e.ems[1])
		rhs[i] = e.ms - msR - e.ems[2]*iv.value
	}
	x, ok := mat.SolveSquare(a, rhs)
	out["part"] = vcInfo{estimable: ok}
	out["operator"] = vcInfo{estimable: ok}
	if ok {
		out["part"] = vcInfo{value: x[0], estimable: true}
		out["operator"] = vcInfo{value: x[1], estimable: true}
	}
	return out
}

func dfPos(msR float64) bool { return isFiniteF(msR) }

// attachFTest fills F and p. Interaction is tested exactly against repeat
// error; part/operator use an EMS-matching Satterthwaite synthesis.
func attachFTest(row *ANOVARow, target extLine, rows []extLine, msR, dfR float64) {
	if row.Source == "interaction" {
		if msR > 0 && target.df > 0 {
			ff := target.ms / msR
			pp := FSurv(ff, target.df, dfR)
			row.F = ptr(ff)
			row.P = ptr(pp)
		}
		return
	}
	lambda, dfDen, approx := synthesize(target.ems, rows, msR, dfR)
	if lambda <= 0 || dfDen <= 0 {
		return
	}
	ff := target.ms / lambda
	pp := FSurv(ff, target.df, dfDen)
	row.F = ptr(ff)
	row.P = ptr(pp)
	if approx {
		row.Note = "F denominator synthesized by Satterthwaite/EMS matching (approximate for this unbalanced pattern)"
	}
}

// synthesize finds nonnegative coefficients over all mean squares so that
// their linear combination matches the target EMS vector. Subsets are
// enumerated; the 4-row EMS system is solved by least squares and the
// closest nonnegative match wins (fewer terms on ties). Returns the matched
// denominator MS, its Satterthwaite degrees of freedom and an "approximate"
// flag when the match is not essentially exact.
func synthesize(target [4]float64, rows []extLine, msR, dfR float64) (lambda, dfDen float64, approx bool) {
	type cand struct {
		df, ms float64
		ems    [4]float64
	}
	cands := make([]cand, 0, len(rows)+1)
	for _, r := range rows {
		cands = append(cands, cand{r.df, r.ms, r.ems})
	}
	cands = append(cands, cand{dfR, msR, [4]float64{0, 0, 0, 1}})
	nc := len(cands)
	bestScore := 1e300
	var bestLam []float64
	for mask := 1; mask < (1 << nc); mask++ {
		idx := []int{}
		for k := 0; k < nc; k++ {
			if mask&(1<<k) != 0 {
				idx = append(idx, k)
			}
		}
		a := mat.New(4, len(idx))
		for col, k := range idx {
			for r := 0; r < 4; r++ {
				a.Set(r, col, cands[k].ems[r])
			}
		}
		bvec := []float64{target[0], target[1], target[2], target[3]}
		x, ok := mat.LeastSquares(a, bvec)
		if !ok {
			continue
		}
		good := true
		for _, xi := range x {
			if xi < -1e-9 {
				good = false
				break
			}
		}
		if !good {
			continue
		}
		res, scale := 0.0, 1e-12
		for r := 0; r < 4; r++ {
			s := 0.0
			for col, k := range idx {
				s += x[col] * cands[k].ems[r]
			}
			d := s - bvec[r]
			res += d * d
			if v := absF(bvec[r]); v > scale {
				scale = v
			}
		}
		score := res/(scale*scale) + 1e-9*float64(len(idx))
		if score < bestScore {
			bestScore = score
			full := make([]float64, nc)
			for col, k := range idx {
				full[k] = x[col]
			}
			bestLam = full
		}
	}
	if bestLam == nil {
		return 0, 0, true
	}
	lam, num, den := 0.0, 0.0, 0.0
	for k, lk := range bestLam {
		if lk <= 0 || cands[k].ms <= 0 {
			continue
		}
		t := lk * cands[k].ms
		lam += t
		num += t * t
		den += t * t / cands[k].df
	}
	if den <= 0 {
		return 0, 0, true
	}
	return lam, num / den, bestScore > 1e-7
}

func isFiniteF(x float64) bool { return x == x && x < 1e300 && x > -1e300 }

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
