package msa

import "math"

// layout describes a balanced r = p*o*n design.
type layout struct {
	parts, ops []string
	p, o, n    int
	// cell[i][j] holds the n readings for parts[i] x ops[j], in trial order.
	cell [][][]float64
}

// balResult is the closed-form balanced ANOVA output.
type balResult struct {
	rows     []ANOVARow
	rawComps map[string]float64 // part, operator, interaction, repeat
}

// balancedANOVA fits the two-factor crossed design with replication:
// SSpart, SSoperator, SSinteraction, SSrepeat sum exactly to SSTotal.
func balancedANOVA(lay *layout, pooled bool) *balResult {
	p, o, n := lay.p, lay.o, lay.n
	yGrand := 0.0
	rowSum := make([]float64, p)
	colSum := make([]float64, o)
	cellSum := make([][]float64, p)
	for i := 0; i < p; i++ {
		cellSum[i] = make([]float64, o)
	}
	N := p * o * n
	for i := 0; i < p; i++ {
		for j := 0; j < o; j++ {
			for k := 0; k < n; k++ {
				y := lay.cell[i][j][k]
				yGrand += y
				rowSum[i] += y
				colSum[j] += y
				cellSum[i][j] += y
			}
		}
	}
	grand := yGrand / float64(N)

	ssP, ssO, ssI, ssE := 0.0, 0.0, 0.0, 0.0
	for i := 0; i < p; i++ {
		ssP += math.Pow(rowSum[i]/float64(o*n)-grand, 2)
	}
	ssP *= float64(o * n)
	for j := 0; j < o; j++ {
		ssO += math.Pow(colSum[j]/float64(p*n)-grand, 2)
	}
	ssO *= float64(p * n)
	for i := 0; i < p; i++ {
		for j := 0; j < o; j++ {
			ssI += math.Pow(cellSum[i][j]/float64(n)-
				rowSum[i]/float64(o*n)-
				colSum[j]/float64(p*n)+grand, 2)
		}
	}
	ssI *= float64(n)
	for i := 0; i < p; i++ {
		for j := 0; j < o; j++ {
			cMean := cellSum[i][j] / float64(n)
			for k := 0; k < n; k++ {
				ssE += math.Pow(lay.cell[i][j][k]-cMean, 2)
			}
		}
	}

	rows := make([]ANOVARow, 0, 6)
	if pooled {
		// Interaction folded into repeat (error) per AIAG rule.
		ssEp := ssE + ssI
		dfEp := float64(p*o*(n-1) + (p-1)*(o-1))
		msEp := ssEp / dfEp
		dfP, dfO := float64(p-1), float64(o-1)
		msP, msO := ssP/dfP, ssO/dfO
		fP, fO := msP/msEp, msO/msEp
		pP, pO := FSurv(fP, dfP, dfEp), FSurv(fO, dfO, dfEp)
		rows = append(rows,
			anovaRow("part", ssP, dfP, ptr(msP), ptr(fP), ptr(pP), []float64{float64(o * n), 0, 0, 1}),
			anovaRow("operator", ssO, dfO, ptr(msO), ptr(fO), ptr(pO), []float64{0, float64(p * n), 0, 1}),
			anovaRow("repeatability", ssEp, dfEp, ptr(msEp), nil, nil, []float64{0, 0, 0, 1}),
			anovaRow("total", ssP+ssO+ssEp, float64(N-1), nil, nil, nil, nil),
		)
		return &balResult{rows: rows, rawComps: map[string]float64{
			"part":     (msP - msEp) / float64(o*n),
			"operator": (msO - msEp) / float64(p*n),
			"repeat":   msEp,
			"interact": 0,
		}}
	}

	dfP, dfO, dfI, dfE := float64(p-1), float64(o-1), float64((p-1)*(o-1)), float64(p*o*(n-1))
	msP, msO := ssP/dfP, ssO/dfO
	msI, msE := ssI/dfI, ssE/dfE
	fI := msI / msE
	pI := FSurv(fI, dfI, dfE)
	rows = append(rows,
		anovaRow("part", ssP, dfP, ptr(msP), nil, nil, []float64{float64(o * n), 0, float64(n), 1}),
		anovaRow("operator", ssO, dfO, ptr(msO), nil, nil, []float64{0, float64(p * n), float64(n), 1}),
		anovaRow("interaction", ssI, dfI, ptr(msI), ptr(fI), ptr(pI), []float64{0, 0, float64(n), 1}),
		anovaRow("repeatability", ssE, dfE, ptr(msE), nil, nil, []float64{0, 0, 0, 1}),
		anovaRow("total", ssP+ssO+ssI+ssE, float64(N-1), nil, nil, nil, nil),
	)
	return &balResult{rows: rows, rawComps: map[string]float64{
		"part":     (msP - msI) / float64(o*n),
		"operator": (msO - msI) / float64(p*n),
		"interact": (msI - msE) / float64(n),
		"repeat":   msE,
	}}
}

func anovaRow(source string, ss, df float64, ms, f, pv *float64, ems []float64) ANOVARow {
	return ANOVARow{Source: source, SS: ss, DF: df, MS: ms, F: f, P: pv, EMS: ems}
}

func ptr(x float64) *float64 { return &x }

// detectBalanced returns a layout iff every part×operator cell has the same
// number n>=2 of trials and all triples are present.
func detectBalanced(data []Measurement) (*layout, bool) {
	type key struct{ p, o string }
	cells := map[key][]float64{}
	parts, ops := []string{}, []string{}
	seenP, seenO := map[string]bool{}, map[string]bool{}
	maxTrial := 0
	for _, m := range data {
		k := key{m.PartID, m.OperatorID}
		cells[k] = append(cells[k], m.Value)
		if !seenP[m.PartID] {
			seenP[m.PartID] = true
			parts = append(parts, m.PartID)
		}
		if !seenO[m.OperatorID] {
			seenO[m.OperatorID] = true
			ops = append(ops, m.OperatorID)
		}
		if m.Trial > maxTrial {
			maxTrial = m.Trial
		}
	}
	p, o := len(parts), len(ops)
	if p < 2 || o < 2 || len(cells) != p*o {
		return nil, false
	}
	n := len(cells[key{parts[0], ops[0]}])
	if n < 2 {
		return nil, false
	}
	lay := &layout{parts: parts, ops: ops, p: p, o: o, n: n}
	lay.cell = make([][][]float64, p)
	for i := range lay.cell {
		lay.cell[i] = make([][]float64, o)
	}
	idxP, idxO := indexMap(parts), indexMap(ops)
	for k, vals := range cells {
		if len(vals) != n {
			return nil, false
		}
		lay.cell[idxP[k.p]][idxO[k.o]] = vals
	}
	return lay, true
}

// dropToBalanced implements the "remove incomplete parts" strategy. A kept
// part must be measured by every operator with the same repeat count n (>=2).
// The largest complete set wins; ties prefer the bigger n.
func dropToBalanced(data []Measurement) (kept []Measurement, removed []RemovedReading, parts, ops []string, n int) {
	type key struct{ p, o string }
	cellN := map[key]int{}
	cellVals := map[key][]Measurement{}
	partSet, opSet := map[string]bool{}, map[string]bool{}
	for _, m := range data {
		k := key{m.PartID, m.OperatorID}
		cellN[k]++
		cellVals[k] = append(cellVals[k], m)
		partSet[m.PartID] = true
		opSet[m.OperatorID] = true
	}
	ops = sortedKeys(opSet)
	// Count how many complete parts exist at each common repeat number.
	// A part is complete if every operator measured it with the same n>=2.
	counts := map[int][]string{}
	for pi := range partSet {
		ns := map[int]int{}
		complete := true
		for _, op := range ops {
			cn := cellN[key{pi, op}]
			if cn < 2 {
				complete = false
				break
			}
			ns[cn]++
		}
		if !complete || len(ns) != 1 {
			continue
		}
		cn := 0
		for k := range ns {
			cn = k
		}
		counts[cn] = append(counts[cn], pi)
	}
	bestN, bestList := 0, []string(nil)
	for cn, list := range counts {
		if len(list) > len(bestList) || (len(list) == len(bestList) && cn > bestN) {
			bestN, bestList = cn, list
		}
	}
	parts = sortedStringSlice(bestList)
	n = bestN
	keep := map[string]bool{}
	for _, pi := range parts {
		keep[pi] = true
	}
	for _, m := range data {
		if keep[m.PartID] {
			kept = append(kept, m)
		} else {
			removed = append(removed, RemovedReading{
				PartID: m.PartID, OperatorID: m.OperatorID, Trial: m.Trial,
				Value: m.Value, Reason: "part incomplete or not measured by every operator with equal replicates; dropped to balance design",
			})
		}
	}
	return kept, removed, parts, ops, n
}

func indexMap(s []string) map[string]int {
	m := make(map[string]int, len(s))
	for i, v := range s {
		m[v] = i
	}
	return m
}
