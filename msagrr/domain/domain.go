// Package domain holds the persisted entities of the Gauge R&R service.
package domain

import "time"

// Status of a study version.
const (
	StatusOpen      = "open"
	StatusFinalized = "finalized"
)

// Study groups all versions of one measurement-system study. A study is
// bound to exactly one gauge and one characteristic with its tolerance.
type Study struct {
	ID             string    `json:"id"`
	GaugeID        string    `json:"gauge_id"`
	GaugeName      string    `json:"gauge_name,omitempty"`
	Characteristic string    `json:"characteristic"`
	Tolerance      float64   `json:"tolerance"`
	CurrentVersion string    `json:"current_version_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Version is an immutable-on-finalize data set. Returning to a finalized
// study creates a new version; old versions and their results stay
// untouched.
type Version struct {
	ID            string    `json:"id"`
	StudyID       string    `json:"study_id"`
	VersionNo     int       `json:"version_no"`
	Status        string    `json:"status"`
	Revision      int64     `json:"revision"` // bumped on every data mutation; used for optimistic concurrency
	Fingerprint   string    `json:"fingerprint"`
	CreatedAt     time.Time `json:"created_at"`
	FinalizedAt   time.Time `json:"finalized_at,omitempty"`
	ParentVersion string    `json:"parent_version_id,omitempty"`
	Notes         string    `json:"notes,omitempty"`
}

// Measurement is one reading. It can be entered singly or imported in a
// batch. Revision supports If-Match conflict detection on update.
type Measurement struct {
	ID         string    `json:"id"`
	VersionID  string    `json:"version_id"`
	Part       string    `json:"part"`
	Operator   string    `json:"operator"`
	Trial      int       `json:"trial"`
	Value      float64   `json:"value"`
	RecordedAt time.Time `json:"recorded_at"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Revision   int64     `json:"revision"`
}

// Result is an ANOVA computation bound to an exact data fingerprint.
// Results are never overwritten: once the data change, stale=true and a
// new result is stored.
type Result struct {
	ID          string    `json:"id"`
	VersionID   string    `json:"version_id"`
	StudyID     string    `json:"study_id"`
	Fingerprint string    `json:"data_fingerprint"`
	Stale       bool      `json:"stale"`
	CreatedAt   time.Time `json:"created_at"`
	Payload     string    `json:"payload"` // JSON AnalysisResult
}
