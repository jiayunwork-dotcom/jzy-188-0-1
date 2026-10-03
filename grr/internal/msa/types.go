// Package msa implements Measurement System Analysis (AIAG-style crossed
// Gage R&R) by the ANOVA method.
//
// The design is part (a) × operator (b) crossed, with r(ab) repeat trials.
// Balanced data are handled by the classical closed-form two-factor ANOVA
// with replication; unbalanced/missing data are handled by a general engine
// based on Henderson's Method I (sequential sums of squares from orthogonal
// projection), with expected mean squares derived by matrix traces so the
// same code covers every missingness pattern. Both routes reproduce the
// textbook balanced ANOVA table exactly.
package msa

import "math"

// Measurement is one recorded reading. PartID/OperatorID are labels (any
// string); Trial is the 1-based repeat number.
type Measurement struct {
	PartID     string
	OperatorID string
	Trial      int
	Value      float64
}

// Method selects the estimation strategy.
type Method string

const (
	// MethodAuto uses the general Henderson Method I engine on every
	// retained reading (default).
	MethodAuto Method = "henderson"
	// MethodBalancedDrop discards incomplete parts and computes on the
	// remaining balanced subset with the classical ANOVA.
	MethodBalancedDrop Method = "balanced_drop"
)

// FieldError identifies an invalid input field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// ValidationError aggregates field-level problems.
type ValidationError struct {
	Errors []FieldError `json:"errors"`
}

func (e *ValidationError) Error() string {
	if len(e.Errors) == 0 {
		return "validation failed"
	}
	return "validation failed: " + e.Errors[0].Error()
}

// Has reports whether the given field has an error.
func (e *ValidationError) Has(field string) bool {
	for _, fe := range e.Errors {
		if fe.Field == field {
			return true
		}
	}
	return false
}

// ANOVARow is one source line of the ANOVA table.
type ANOVARow struct {
	Source       string   `json:"source"`
	SS           float64  `json:"ss"`
	DF           float64  `json:"df"`
	MS           *float64 `json:"ms,omitempty"`
	F            *float64 `json:"f,omitempty"`
	P            *float64 `json:"p,omitempty"`
	Note         string   `json:"note,omitempty"`
	NonEstimable bool     `json:"non_estimable,omitempty"`
	// EMS coefficients in the order [part, operator, interaction, repeat].
	EMS []float64 `json:"ems_coefficients,omitempty"`
}

// VarComp is one estimated variance component.
type VarComp struct {
	Name      string  `json:"name"`
	Estimate  float64 `json:"estimate"`
	Raw       float64 `json:"raw_estimate"`
	Clipped   bool    `json:"clipped_to_zero"`
	Estimable bool    `json:"estimable"`
}

// Stat bundles a standard deviation with its common percentage expressions.
type Stat struct {
	Name              string   `json:"name"`
	SD                float64  `json:"sd"`
	Variance          float64  `json:"variance"`
	PctTotalVariance  *float64 `json:"pct_total_variance,omitempty"`
	PctStudyVariation *float64 `json:"pct_study_variation,omitempty"`
	PctTolerance      *float64 `json:"pct_tolerance,omitempty"`
}

// Metrics collects the reported Gage R&R statistics.
type Metrics struct {
	Repeatability     Stat    `json:"repeatability"`        // EV
	Reproducibility   Stat    `json:"reproducibility"`      // AV
	GRR               Stat    `json:"grr"`                  // sqrt(EV2+AV2)
	PartVariation     Stat    `json:"part_variation"`       // PV
	TotalVariation    Stat    `json:"total_variation"`      // TV
	PctGRROfTotal     float64 `json:"pct_grr_of_total"`     // %GRR relative to total
	PctGRROfTolerance float64 `json:"pct_grr_of_tolerance"` // %GRR relative to tolerance
	NDC               int     `json:"ndc"`                  // number of distinct categories
}

// CellSummary describes one part×operator cell for the data-usage report.
type CellSummary struct {
	PartID     string `json:"part_id"`
	OperatorID string `json:"operator_id"`
	Replicates int    `json:"replicates"`
}

// RemovedReading explains one reading excluded from the computation.
type RemovedReading struct {
	PartID     string  `json:"part_id"`
	OperatorID string  `json:"operator_id"`
	Trial      int     `json:"trial"`
	Value      float64 `json:"value"`
	Reason     string  `json:"reason"`
}

// DataUsage states exactly which readings the result is based on.
type DataUsage struct {
	Method            Method           `json:"method"`
	MethodReason      string           `json:"method_reason"`
	TotalReadings     int              `json:"total_readings"`
	UsedReadings      int              `json:"used_readings"`
	Removed           []RemovedReading `json:"removed_readings,omitempty"`
	Parts             []string         `json:"parts"`
	Operators         []string         `json:"operators"`
	Cells             []CellSummary    `json:"cells"`
	TrialsPerCell     int              `json:"trials_per_cell"`
	Balanced          bool             `json:"balanced"`
	InteractionPooled bool             `json:"interaction_pooled"`
	PoolReason        string           `json:"pool_reason,omitempty"`
	Notes             []string         `json:"notes,omitempty"`
}

// SSDiagnostic reports the additivity audit: component SS must sum to SST.
type SSDiagnostic struct {
	TotalSS       float64 `json:"total_ss"`
	SumComponents float64 `json:"sum_components"`
	Diff          float64 `json:"abs_diff"`
	RelError      float64 `json:"rel_error"`
	WithinTol     bool    `json:"within_1e_9"`
}

// Result is a version-bound analysis output.
type Result struct {
	Tolerance float64      `json:"tolerance"`
	DataHash  string       `json:"data_hash"`
	ANOVA     []ANOVARow   `json:"anova"`
	VarComps  []VarComp    `json:"variance_components"`
	Metrics   Metrics      `json:"metrics"`
	DataUsage DataUsage    `json:"data_usage"`
	SSCheck   SSDiagnostic `json:"ss_partition_check"`
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
