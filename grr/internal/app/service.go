// Package app wires storage, the MSA engine and the HTTP-facing rules:
// optimistic concurrency, finalized-version write protection, result/data
// version binding and staleness.
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/automotive/grr/internal/msa"
	"github.com/automotive/grr/internal/store"
)

// Service is the application facade.
type Service struct {
	st *store.Store
}

func New(st *store.Store) *Service { return &Service{st: st} }

// newID returns a 24-hex-char random identifier.
func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- studies ----

type StudyInput struct {
	GageID         string  `json:"gage_id"`
	Characteristic string  `json:"characteristic"`
	Tolerance      float64 `json:"tolerance"`
}

func (in *StudyInput) validate() *msa.ValidationError {
	ve := &msa.ValidationError{}
	if strings.TrimSpace(in.GageID) == "" {
		ve.Errors = append(ve.Errors, msa.FieldError{Field: "gage_id", Message: "gage_id is required"})
	}
	if strings.TrimSpace(in.Characteristic) == "" {
		ve.Errors = append(ve.Errors, msa.FieldError{Field: "characteristic", Message: "characteristic is required"})
	}
	if !isFinite(in.Tolerance) || in.Tolerance <= 0 {
		ve.Errors = append(ve.Errors, msa.FieldError{Field: "tolerance", Message: "tolerance must be a positive finite number"})
	}
	if len(ve.Errors) > 0 {
		return ve
	}
	return nil
}

func isFinite(x float64) bool { return x == x && x < 1e300 && x > -1e300 }

// CreateStudy makes a study with draft version 1.
func (s *Service) CreateStudy(ctx context.Context, in StudyInput) (*store.Study, *store.Version, error) {
	if ve := in.validate(); ve != nil {
		return nil, nil, ve
	}
	st := &store.Study{
		ID: newID(), GageID: strings.TrimSpace(in.GageID),
		Characteristic: strings.TrimSpace(in.Characteristic),
		Tolerance:      in.Tolerance,
	}
	v, err := s.st.CreateStudy(ctx, st, newID())
	if err != nil {
		return nil, nil, err
	}
	return st, v, nil
}

func (s *Service) GetStudy(ctx context.Context, id string) (*store.Study, error) {
	return s.st.GetStudy(ctx, id)
}

func (s *Service) ListStudies(ctx context.Context, limit, offset int) ([]*store.Study, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.st.ListStudies(ctx, limit, offset)
}

// UpdateStudy edits header fields. Header edits are allowed regardless of
// version state; changing the tolerance on a finalized study does not alter
// any frozen result (results embed their own tolerance and data hash).
func (s *Service) UpdateStudy(ctx context.Context, id string, in StudyInput) (*store.Study, error) {
	if ve := in.validate(); ve != nil {
		return nil, ve
	}
	st, err := s.st.GetStudy(ctx, id)
	if err != nil {
		return nil, err
	}
	st.GageID = strings.TrimSpace(in.GageID)
	st.Characteristic = strings.TrimSpace(in.Characteristic)
	st.Tolerance = in.Tolerance
	if err := s.st.UpdateStudy(ctx, st); err != nil {
		return nil, err
	}
	return st, nil
}

// ---- versions ----

func (s *Service) GetVersion(ctx context.Context, id string) (*store.Version, error) {
	return s.st.GetVersion(ctx, id)
}

func (s *Service) ListVersions(ctx context.Context, studyID string) ([]*store.Version, error) {
	if _, err := s.st.GetStudy(ctx, studyID); err != nil {
		return nil, err
	}
	return s.st.ListVersions(ctx, studyID)
}

func (s *Service) CurrentDraft(ctx context.Context, studyID string) (*store.Version, error) {
	v, err := s.st.CurrentDraft(ctx, studyID)
	if err == store.ErrNotFound {
		return nil, &msa.ValidationError{Errors: []msa.FieldError{{
			Field: "study_id", Message: "study has no draft version; finalize then open a new version",
		}}}
	}
	return v, err
}

// NewVersion clones the given finalized version (or the study's latest
// finalized version when baseID is empty) into a new draft.
func (s *Service) NewVersion(ctx context.Context, studyID, baseID string) (*store.Version, error) {
	if _, err := s.st.GetStudy(ctx, studyID); err != nil {
		return nil, err
	}
	if baseID == "" {
		versions, err := s.st.ListVersions(ctx, studyID)
		if err != nil {
			return nil, err
		}
		for _, v := range versions {
			if v.Status == "finalized" {
				baseID = v.ID
				break
			}
		}
		if baseID == "" {
			return nil, &msa.ValidationError{Errors: []msa.FieldError{{
				Field: "based_on_id", Message: "no finalized version exists to base a re-measurement on",
			}}}
		}
	}
	return s.st.NewVersionFromFinalized(ctx, baseID, newID())
}

// Finalize freezes a version after validating the data it contains.
func (s *Service) Finalize(ctx context.Context, versionID string) (*store.Version, error) {
	v, err := s.st.GetVersion(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if v.Status == "finalized" {
		return v, nil
	}
	st, err := s.st.GetStudy(ctx, v.StudyID)
	if err != nil {
		return nil, err
	}
	ms, err := s.st.ListMeasurements(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if ve := msa.Validate(toEngine(ms), st.Tolerance); ve != nil {
		return nil, ve
	}
	return s.st.FinalizeVersion(ctx, versionID)
}

// ---- measurements ----

type MeasurementInput struct {
	PartID     string     `json:"part_id"`
	OperatorID string     `json:"operator_id"`
	Trial      int        `json:"trial"`
	Value      float64    `json:"value"`
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
}

func validateReading(in *MeasurementInput) *msa.ValidationError {
	ve := &msa.ValidationError{}
	if strings.TrimSpace(in.PartID) == "" {
		ve.Errors = append(ve.Errors, msa.FieldError{Field: "part_id", Message: "part_id is required"})
	}
	if strings.TrimSpace(in.OperatorID) == "" {
		ve.Errors = append(ve.Errors, msa.FieldError{Field: "operator_id", Message: "operator_id is required"})
	}
	if in.Trial < 1 {
		ve.Errors = append(ve.Errors, msa.FieldError{Field: "trial", Message: "trial must be a 1-based positive integer"})
	}
	if !isFinite(in.Value) {
		ve.Errors = append(ve.Errors, msa.FieldError{Field: "value", Message: "value must be a finite number"})
	}
	if len(ve.Errors) > 0 {
		return ve
	}
	return nil
}

func (in *MeasurementInput) toModel(versionID string) *store.Measurement {
	ts := time.Now().UTC()
	if in.RecordedAt != nil {
		ts = in.RecordedAt.UTC()
	}
	return &store.Measurement{
		ID: newID(), VersionID: versionID,
		PartID:     strings.TrimSpace(in.PartID),
		OperatorID: strings.TrimSpace(in.OperatorID),
		Trial:      in.Trial, Value: in.Value, RecordedAt: ts,
	}
}

func (s *Service) ListMeasurements(ctx context.Context, versionID string) ([]*store.Measurement, error) {
	if _, err := s.st.GetVersion(ctx, versionID); err != nil {
		return nil, err
	}
	return s.st.ListMeasurements(ctx, versionID)
}

func (s *Service) AddMeasurement(ctx context.Context, versionID string, in MeasurementInput) (*store.Measurement, error) {
	if ve := validateReading(&in); ve != nil {
		return nil, ve
	}
	m := in.toModel(versionID)
	if err := s.st.AddMeasurement(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Service) UpdateMeasurement(ctx context.Context, versionID, measurementID string, in MeasurementInput, expectedVersion int) (*store.Measurement, error) {
	if ve := validateReading(&in); ve != nil {
		return nil, ve
	}
	cur, err := s.st.GetMeasurement(ctx, measurementID)
	if err != nil {
		return nil, err
	}
	if cur.VersionID != versionID {
		return nil, store.ErrNotFound
	}
	cur.PartID = strings.TrimSpace(in.PartID)
	cur.OperatorID = strings.TrimSpace(in.OperatorID)
	cur.Trial = in.Trial
	cur.Value = in.Value
	if in.RecordedAt != nil {
		cur.RecordedAt = in.RecordedAt.UTC()
	}
	if err := s.st.UpdateMeasurement(ctx, cur, expectedVersion); err != nil {
		return nil, err
	}
	return cur, nil
}

func (s *Service) DeleteMeasurement(ctx context.Context, versionID, measurementID string) error {
	cur, err := s.st.GetMeasurement(ctx, measurementID)
	if err != nil {
		return err
	}
	if cur.VersionID != versionID {
		return store.ErrNotFound
	}
	return s.st.DeleteMeasurement(ctx, measurementID)
}

// ImportRequest is a batch import.
type ImportRequest struct {
	Readings []MeasurementInput `json:"readings"`
}

func (s *Service) Import(ctx context.Context, versionID string, req ImportRequest) (int, error) {
	if len(req.Readings) == 0 {
		return 0, &msa.ValidationError{Errors: []msa.FieldError{{
			Field: "readings", Message: "at least one reading is required",
		}}}
	}
	// Validate all rows first (including in-batch duplicates) so a bad
	// import never partially lands.
	seen := map[string]int{}
	models := make([]*store.Measurement, 0, len(req.Readings))
	ve := &msa.ValidationError{}
	for i := range req.Readings {
		in := &req.Readings[i]
		rowErrs := validateReading(in)
		if rowErrs != nil {
			for _, fe := range rowErrs.Errors {
				fe.Field = fmt.Sprintf("readings[%d].%s", i, fe.Field)
				ve.Errors = append(ve.Errors, fe)
			}
		} else {
			key := in.PartID + "|" + in.OperatorID + "|" + fmt.Sprint(in.Trial)
			if first, dup := seen[key]; dup {
				ve.Errors = append(ve.Errors, msa.FieldError{
					Field:   fmt.Sprintf("readings[%d]", i),
					Message: fmt.Sprintf("duplicate of readings[%d] for the same part/operator/trial", first),
				})
			} else {
				seen[key] = i
				models = append(models, in.toModel(versionID))
			}
		}
	}
	if len(ve.Errors) > 0 {
		return 0, ve
	}
	if err := s.st.BatchAddMeasurements(ctx, versionID, models); err != nil {
		return 0, err
	}
	return len(models), nil
}

// ---- compute ----

// ComputeRequest optionally selects the estimation method.
type ComputeRequest struct {
	Method msa.Method `json:"method"`
}

// ComputeResult bundles the stored result record with the decoded payload.
type ComputeResult struct {
	ID        string      `json:"id"`
	VersionID string      `json:"version_id"`
	DataHash  string      `json:"data_hash"`
	Stale     bool        `json:"stale"`
	CreatedAt time.Time   `json:"created_at"`
	Result    *msa.Result `json:"result"`
}

func toEngine(ms []*store.Measurement) []msa.Measurement {
	out := make([]msa.Measurement, 0, len(ms))
	for _, m := range ms {
		out = append(out, msa.Measurement{
			PartID: m.PartID, OperatorID: m.OperatorID,
			Trial: m.Trial, Value: m.Value,
		})
	}
	return out
}

// DataHash is the SHA-256 of a canonical serialization of tolerance plus
// every reading, independent of input ordering.
func DataHash(tolerance float64, data []msa.Measurement) string {
	rows := make([]msa.Measurement, len(data))
	copy(rows, data)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].PartID != rows[j].PartID {
			return rows[i].PartID < rows[j].PartID
		}
		if rows[i].OperatorID != rows[j].OperatorID {
			return rows[i].OperatorID < rows[j].OperatorID
		}
		return rows[i].Trial < rows[j].Trial
	})
	var b strings.Builder
	fmt.Fprintf(&b, "tol=%.17g\n", tolerance)
	for _, r := range rows {
		fmt.Fprintf(&b, "%s\t%s\t%d\t%.17g\n", r.PartID, r.OperatorID, r.Trial, r.Value)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Compute runs the analysis on the version's current readings and persists
// the result. Legal on any version: a finalized version computes a fresh
// read-only view without touching stored data.
func (s *Service) Compute(ctx context.Context, versionID string, method msa.Method) (*ComputeResult, error) {
	v, err := s.st.GetVersion(ctx, versionID)
	if err != nil {
		return nil, err
	}
	st, err := s.st.GetStudy(ctx, v.StudyID)
	if err != nil {
		return nil, err
	}
	ms, err := s.st.ListMeasurements(ctx, versionID)
	if err != nil {
		return nil, err
	}
	data := toEngine(ms)
	res, err := msa.Analyze(data, st.Tolerance, method)
	if err != nil {
		return nil, err
	}
	hash := DataHash(st.Tolerance, data)
	res.DataHash = hash
	payload, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	rec := &store.Result{ID: newID(), VersionID: versionID, DataHash: hash, Payload: string(payload)}
	if err := s.st.SaveResult(ctx, rec); err != nil {
		return nil, err
	}
	return &ComputeResult{
		ID: rec.ID, VersionID: versionID, DataHash: hash,
		CreatedAt: time.Now().UTC(), Result: res,
	}, nil
}

// ListResults returns every result computed on a version.
func (s *Service) ListResults(ctx context.Context, versionID string) ([]*ComputeResult, error) {
	if _, err := s.st.GetVersion(ctx, versionID); err != nil {
		return nil, err
	}
	recs, err := s.st.ListResults(ctx, versionID)
	if err != nil {
		return nil, err
	}
	out := make([]*ComputeResult, 0, len(recs))
	for _, r := range recs {
		cr, err := decodeResult(r)
		if err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	return out, nil
}

func decodeResult(r *store.Result) (*ComputeResult, error) {
	var res msa.Result
	if err := json.Unmarshal([]byte(r.Payload), &res); err != nil {
		return nil, err
	}
	return &ComputeResult{
		ID: r.ID, VersionID: r.VersionID, DataHash: r.DataHash,
		Stale: r.Stale, CreatedAt: r.CreatedAt, Result: &res,
	}, nil
}
