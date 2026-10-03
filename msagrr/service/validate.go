package service

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// isFinite reports whether v is neither NaN nor an infinity. (math.IsFinite
// only exists from Go 1.24; this service targets Go 1.23.)
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// FieldError points to the exact request field that failed validation.
type FieldError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// ValidationError aggregates field errors.
type ValidationError struct {
	Fields []FieldError `json:"field_errors"`
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	b.WriteString("validation failed: ")
	for i, f := range e.Fields {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(f.Field + " " + f.Reason)
	}
	return b.String()
}

func (e *ValidationError) add(field, reason string) {
	e.Fields = append(e.Fields, FieldError{Field: field, Reason: reason})
}

// CreateStudyRequest is the payload for POST /studies.
type CreateStudyRequest struct {
	GaugeID        string   `json:"gauge_id"`
	GaugeName      string   `json:"gauge_name"`
	Characteristic string   `json:"characteristic"`
	Tolerance      *float64 `json:"tolerance"`
}

// ValidateCreateStudy checks the study creation payload.
func ValidateCreateStudy(r CreateStudyRequest) error {
	ve := &ValidationError{}
	if strings.TrimSpace(r.GaugeID) == "" {
		ve.add("gauge_id", "must not be blank")
	}
	if strings.TrimSpace(r.Characteristic) == "" {
		ve.add("characteristic", "must not be blank")
	}
	if r.Tolerance == nil {
		ve.add("tolerance", "is required")
	} else if !isFinite(*r.Tolerance) {
		ve.add("tolerance", "must be a finite number")
	} else if *r.Tolerance <= 0 {
		ve.add("tolerance", "must be positive")
	}
	if len(ve.Fields) > 0 {
		return ve
	}
	return nil
}

// ReadingRequest is one reading in single or batch endpoints.
type ReadingRequest struct {
	Part       string     `json:"part"`
	Operator   string     `json:"operator"`
	Trial      *int       `json:"trial"`
	Value      *float64   `json:"value"`
	RecordedAt *time.Time `json:"recorded_at"`
}

// ValidateReading validates one reading. index, when >= 0, prefixes the
// field path with the batch position, e.g. "readings[3].value".
func ValidateReading(r ReadingRequest, index int) FieldError {
	pfx := ""
	if index >= 0 {
		pfx = fmt.Sprintf("readings[%d].", index)
	}
	switch {
	case strings.TrimSpace(r.Part) == "":
		return FieldError{Field: pfx + "part", Reason: "must not be blank"}
	case strings.TrimSpace(r.Operator) == "":
		return FieldError{Field: pfx + "operator", Reason: "must not be blank"}
	case r.Trial == nil:
		return FieldError{Field: pfx + "trial", Reason: "is required"}
	case *r.Trial < 1:
		return FieldError{Field: pfx + "trial", Reason: "must be >= 1"}
	case r.Value == nil:
		return FieldError{Field: pfx + "value", Reason: "is required"}
	case !isFinite(*r.Value):
		return FieldError{Field: pfx + "value", Reason: "must be a finite number (not NaN/Inf)"}
	}
	return FieldError{}
}

// ValidateBatch validates a batch of readings.
func ValidateBatch(rs []ReadingRequest) error {
	ve := &ValidationError{}
	if len(rs) == 0 {
		ve.add("readings", "must contain at least one reading")
		return ve
	}
	for i, r := range rs {
		if fe := ValidateReading(r, i); fe.Field != "" {
			ve.Fields = append(ve.Fields, fe)
		}
	}
	if len(ve.Fields) > 0 {
		return ve
	}
	return nil
}

// UpdateReadingRequest is the partial update payload.
type UpdateReadingRequest struct {
	Value      *float64   `json:"value"`
	RecordedAt *time.Time `json:"recorded_at"`
	Revision   *int64     `json:"revision"`
}

// ValidateUpdate validates the partial update payload.
func ValidateUpdate(r UpdateReadingRequest) (*int64, error) {
	ve := &ValidationError{}
	if r.Value == nil {
		ve.add("value", "is required")
	} else if !isFinite(*r.Value) {
		ve.add("value", "must be a finite number")
	}
	if r.Revision == nil {
		ve.add("revision", "is required for optimistic concurrency control")
	} else if *r.Revision < 1 {
		ve.add("revision", "must be >= 1")
	}
	if len(ve.Fields) > 0 {
		return nil, ve
	}
	return r.Revision, nil
}
