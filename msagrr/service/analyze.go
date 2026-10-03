package service

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"msagrr/domain"
	"msagrr/stat"
)

// AnalysisResult is the full, self-describing output of one ANOVA run.
// It records exactly which data were used, what was dropped, the ANOVA
// table with pooling decision, variance components and all Gauge R&R
// metrics. JSON tags use snake_case for the tablets/clients.
type AnalysisResult struct {
	ID              string    `json:"id,omitempty"`
	Method          string    `json:"method"`
	MethodNote      string    `json:"method_note"`
	StudyID         string    `json:"study_id"`
	VersionID       string    `json:"version_id"`
	VersionNo       int       `json:"version_no"`
	DataFingerprint string    `json:"data_fingerprint"`
	GeneratedAt     time.Time `json:"generated_at"`
	GaugeID         string    `json:"gauge_id"`
	Characteristic  string    `json:"characteristic"`
	Tolerance       float64   `json:"tolerance"`

	Design DesignInfo `json:"design"`
	Data   DataInfo   `json:"data_used"`

	ANOVA      []ANOVACell `json:"anova"`
	Pooling    PoolingInfo `json:"pooling"`
	Components []Component `json:"variance_components"`
	Metrics    GRRMetrics  `json:"metrics"`
	Notes      []string    `json:"notes"`
}

// DesignInfo describes the balanced design actually analyzed.
type DesignInfo struct {
	Parts        int `json:"parts"`
	Operators    int `json:"operators"`
	Repeats      int `json:"repeats_per_cell"`
	Observations int `json:"observations"`
}

// DataInfo states what went into the analysis.
type DataInfo struct {
	Parts        []string               `json:"parts"`
	Operators    []string               `json:"operators"`
	TrialNumbers []int                  `json:"trial_numbers"`
	RawCount     int                    `json:"raw_readings"`
	UsedCount    int                    `json:"used_readings"`
	Excluded     []stat.ExcludedReading `json:"excluded_readings"`
	Missing      []stat.MissingCell     `json:"missing_slots"`
}

// ANOVACell is one row of the ANOVA table.
type ANOVACell struct {
	Source string   `json:"source"`
	SS     float64  `json:"sum_of_squares"`
	DF     int      `json:"degrees_of_freedom"`
	MS     float64  `json:"mean_square"`
	F      *float64 `json:"f_ratio"`
	P      *float64 `json:"p_value"`
}

// PoolingInfo documents the interaction pooling decision.
type PoolingInfo struct {
	InteractionP *float64 `json:"interaction_p_value"`
	Threshold    float64  `json:"threshold"`
	Pooled       bool     `json:"pooled_into_repeatability"`
	Reason       string   `json:"reason"`
}

// Component is one variance component.
type Component struct {
	Name          string  `json:"name"`
	Variance      float64 `json:"variance"`
	SD            float64 `json:"std_dev"`
	PctOfTotal    float64 `json:"percent_of_total_variance"`
	ClampedToZero bool    `json:"negative_estimate_clamped_to_zero,omitempty"`
}

// GRRMetrics are the headline Gauge R&R numbers.
type GRRMetrics struct {
	SDRepeat          float64  `json:"repeatability_sd"`
	SDReproducibility float64  `json:"reproducibility_sd"`
	SDGRR             float64  `json:"grr_sd"`
	SDPartVariation   float64  `json:"part_variation_sd"`
	SDTotal           float64  `json:"total_variation_sd"`
	PctGRRTotal       float64  `json:"percent_grr_of_total_variation"`
	PctGRRTolerance   float64  `json:"percent_grr_of_tolerance"`
	NDC               *float64 `json:"ndc_distinct_categories"`
}

func ptrFloat(v float64) *float64 {
	if math.IsNaN(v) {
		return nil
	}
	x := v
	return &x
}

// RawReading is the analysis input in label space.
type RawReading struct {
	Part     string
	Operator string
	Trial    int
	Value    float64
}

// Analyze runs the complete analysis pipeline: generalized complete-case
// filtering to a balanced design, then the classical crossed ANOVA.
//
// Default method rationale (documented in README and method_note):
// the AIAG MSA reference ANOVA is defined for a balanced crossed design;
// reducing the data to the maximal balanced sub-design keeps the
// textbook EMS identifications exact and guarantees identical results
// to the standard ANOVA on balanced data, while every dropped reading
// and missing slot is reported. This is preferred for gauge R&R audits,
// where an explainable, standards-aligned result matters more than
// using every partial cell.
func Analyze(study *domain.Study, version *domain.Version, raw []RawReading) (*AnalysisResult, error) {
	inputs := make([]struct {
		Part     string
		Operator string
		Trial    int
		Value    float64
	}, len(raw))
	for i, r := range raw {
		inputs[i] = struct {
			Part     string
			Operator string
			Trial    int
			Value    float64
		}{r.Part, r.Operator, r.Trial, r.Value}
	}
	cc, err := stat.FilterCompleteCases(inputs)
	if err != nil {
		return nil, err
	}
	flat := cc.Flatten()
	cfg := stat.Config{PoolingAlpha: 0.25, Tolerance: study.Tolerance}
	eng, err := stat.ComputeBalancedANOVA(flat, len(cc.Parts), len(cc.Operators), cc.R, cfg)
	if err != nil {
		return nil, err
	}

	res := &AnalysisResult{
		Method:          "complete_case_balanced_anova",
		MethodNote:      "Reduced to the maximal balanced crossed sub-design (parts measured by every operator; trial numbers present in every cell), then standard two-way crossed ANOVA with interaction. Matches AIAG MSA ANOVA exactly on balanced data; all excluded readings and missing slots are listed.",
		StudyID:         study.ID,
		VersionID:       version.ID,
		VersionNo:       version.VersionNo,
		DataFingerprint: version.Fingerprint,
		GeneratedAt:     time.Now().UTC(),
		GaugeID:         study.GaugeID,
		Characteristic:  study.Characteristic,
		Tolerance:       study.Tolerance,
		Design: DesignInfo{
			Parts: eng.P, Operators: eng.O, Repeats: eng.R, Observations: eng.N,
		},
		Data: DataInfo{
			Parts:        cc.Parts,
			Operators:    cc.Operators,
			TrialNumbers: trialList(cc.R),
			RawCount:     len(raw),
			UsedCount:    eng.N,
			Excluded:     cc.Excluded,
			Missing:      cc.Missing,
		},
	}
	for _, row := range eng.Rows {
		res.ANOVA = append(res.ANOVA, ANOVACell{
			Source: row.Source, SS: row.SS, DF: row.DF, MS: row.MS,
			F: ptrFloat(row.F), P: ptrFloat(row.P),
		})
	}
	res.Pooling = PoolingInfo{
		InteractionP: ptrFloat(eng.InteractionP),
		Threshold:    eng.PoolingAlpha,
		Pooled:       eng.Pooled,
	}
	if eng.Pooled {
		if math.IsNaN(eng.InteractionP) {
			res.Pooling.Reason = "single repeat per cell: interaction not testable, interaction SS used as the error term"
		} else {
			res.Pooling.Reason = fmt.Sprintf("interaction p-value %.4f > %.2f; interaction mean square pooled into repeatability per AIAG rule", eng.InteractionP, eng.PoolingAlpha)
		}
	} else {
		res.Pooling.Reason = fmt.Sprintf("interaction p-value %.4f <= %.2f; interaction retained as a separate variance component", eng.InteractionP, eng.PoolingAlpha)
	}

	clamped := map[string]bool{}
	for _, n := range eng.NegativeVar {
		clamped[n] = true
	}
	res.Components = append(res.Components,
		Component{
			Name: "total variation (TV)", Variance: eng.SDTV * eng.SDTV,
			SD: eng.SDTV, PctOfTotal: 100,
		},
		Component{
			Name: "GRR (gauge R&R)", Variance: eng.SDGRR * eng.SDGRR,
			SD: eng.SDGRR, PctOfTotal: eng.PctGRR,
		},
		Component{
			Name:       "repeatability (equipment variation, EV)",
			Variance:   eng.VarRepeat,
			SD:         eng.SDRepeat,
			PctOfTotal: eng.PctVarRepeat,
		},
		Component{
			Name:       "reproducibility (appraiser variation, AV)",
			Variance:   eng.SDRepro * eng.SDRepro,
			SD:         eng.SDRepro,
			PctOfTotal: eng.PctRepro,
		},
	)
	if !eng.Pooled {
		res.Components = append(res.Components, Component{
			Name:          "  of which: operator component",
			Variance:      eng.VarOp,
			SD:            math.Sqrt(eng.VarOp),
			PctOfTotal:    eng.PctVarOp,
			ClampedToZero: clamped["operator"],
		}, Component{
			Name:          "  of which: part*operator interaction component",
			Variance:      eng.VarInter,
			SD:            math.Sqrt(eng.VarInter),
			PctOfTotal:    eng.PctVarInter,
			ClampedToZero: clamped["part*operator"],
		})
	} else {
		res.Components = append(res.Components, Component{
			Name:          "  of which: operator component",
			Variance:      eng.VarOp,
			SD:            math.Sqrt(eng.VarOp),
			PctOfTotal:    eng.PctVarOp,
			ClampedToZero: clamped["operator"],
		})
	}
	res.Components = append(res.Components, Component{
		Name: "part variation (PV)", Variance: eng.VarPart,
		SD: eng.SDPV, PctOfTotal: eng.PctVarPart,
		ClampedToZero: clamped["part"],
	})

	res.Metrics = GRRMetrics{
		SDRepeat:          eng.SDRepeat,
		SDReproducibility: eng.SDRepro,
		SDGRR:             eng.SDGRR,
		SDPartVariation:   eng.SDPV,
		SDTotal:           eng.SDTV,
		PctGRRTotal:       nanToZero(eng.PctGRRTotal),
		PctGRRTolerance:   eng.PctGRRTolerance,
		NDC:               ptrFloat(eng.NDC),
	}
	if len(eng.NegativeVar) > 0 {
		res.Notes = append(res.Notes,
			fmt.Sprintf("Negative ANOVA variance estimate(s) for %v set to zero per AIAG practice; corresponding row flagged.", eng.NegativeVar))
	}
	if len(cc.Excluded) > 0 {
		res.Notes = append(res.Notes,
			fmt.Sprintf("%d raw reading(s) excluded to obtain the balanced %d parts x %d operators x %d repeats design; see data_used.excluded_readings.", len(cc.Excluded), eng.P, eng.O, eng.R))
	}
	if len(cc.Missing) > 0 {
		res.Notes = append(res.Notes,
			fmt.Sprintf("%d slot(s) of the balanced design had no reading; see data_used.missing_slots.", len(cc.Missing)))
	}
	return res, nil
}

func trialList(r int) []int {
	out := make([]int, r)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

func nanToZero(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return v
}

// MarshalPayload renders the result for storage.
func (a *AnalysisResult) MarshalPayload() ([]byte, error) {
	return json.Marshal(a)
}
