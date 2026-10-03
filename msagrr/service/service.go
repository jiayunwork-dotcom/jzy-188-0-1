// Package service implements the business logic: studies/versions,
// open-vs-finalized write rules, optimistic concurrency, version cloning
// for return studies, data fingerprints, result append/staleness and the
// ANOVA computation pipeline.
package service

import (
	"encoding/json"
	"time"

	"msagrr/domain"
	"msagrr/store"
)

// Service is the application facade over the embedded store.
type Service struct {
	st *store.Store
}

// New constructs a Service.
func New(st *store.Store) *Service { return &Service{st: st} }

// Store exposes persistence for bootstrap/tests.
func (s *Service) Store() *store.Store { return s.st }

// CreateStudy validates and stores a new study, and opens version 1.
func (s *Service) CreateStudy(req CreateStudyRequest) (*domain.Study, *domain.Version, error) {
	if err := ValidateCreateStudy(req); err != nil {
		return nil, nil, err
	}
	st, err := s.st.CreateStudy(req.GaugeID, req.GaugeName, req.Characteristic, *req.Tolerance)
	if err != nil {
		return nil, nil, err
	}
	v, err := s.st.CreateVersion(st.ID)
	if err != nil {
		return nil, nil, err
	}
	// CreateVersion updated CurrentVersion in storage; refetch the study.
	st, err = s.st.GetStudy(st.ID)
	if err != nil {
		return nil, nil, err
	}
	return st, v, nil
}

// GetStudy returns a study.
func (s *Service) GetStudy(id string) (*domain.Study, error) { return s.st.GetStudy(id) }

// ListStudies returns all studies.
func (s *Service) ListStudies() ([]*domain.Study, error) { return s.st.ListStudies() }

// GetVersion loads a version and verifies it belongs to the study.
func (s *Service) GetVersion(studyID, versionID string) (*domain.Version, error) {
	v, err := s.st.GetVersion(versionID)
	if err != nil {
		return nil, err
	}
	if v.StudyID != studyID {
		return nil, store.ErrNotFound
	}
	return v, nil
}

// ListVersions lists versions of a study.
func (s *Service) ListVersions(studyID string) ([]*domain.Version, error) {
	return s.st.ListVersions(studyID)
}

// StartReturnStudy creates a new open version of a finalized study by
// cloning the chosen finalized version's raw readings. Old versions and
// their results remain intact and queryable.
func (s *Service) StartReturnStudy(studyID, parentVersionID string) (*domain.Version, error) {
	if _, err := s.st.GetStudy(studyID); err != nil {
		return nil, err
	}
	v, err := s.st.CreateVersionFrom(studyID, parentVersionID)
	if err != nil {
		return nil, err
	}
	// Refresh fingerprint of cloned data.
	ms, err := s.st.ListMeasurements(v.ID)
	if err != nil {
		return nil, err
	}
	if err := s.st.SetFingerprint(v.ID, DataFingerprint(ms)); err != nil {
		return nil, err
	}
	v.Fingerprint = DataFingerprint(ms)
	// Point the study's current version at the new return-study version.
	return s.refreshCurrent(studyID, v)
}

func (s *Service) refreshCurrent(studyID string, v *domain.Version) (*domain.Version, error) {
	st, err := s.st.GetStudy(studyID)
	if err != nil {
		return nil, err
	}
	st.CurrentVersion = v.ID
	st.UpdatedAt = time.Now().UTC()
	// No generic study update method exists; use a tiny inline update via
	// a dedicated store call.
	if err := s.st.UpdateStudy(st); err != nil {
		return nil, err
	}
	return v, nil
}

// AddReading adds one measurement to an open version.
func (s *Service) AddReading(versionID string, r ReadingRequest) (*domain.Measurement, error) {
	if fe := ValidateReading(r, -1); fe.Field != "" {
		return nil, &ValidationError{Fields: []FieldError{fe}}
	}
	var ra time.Time
	if r.RecordedAt != nil {
		ra = *r.RecordedAt
	}
	m, err := s.st.AddMeasurement(versionID, r.Part, r.Operator, *r.Trial, *r.Value, ra)
	if err != nil {
		return nil, err
	}
	_ = s.recomputeFingerprintAndStale(versionID)
	return m, nil
}

// AddReadings imports a batch atomically; duplicate slots are rejected.
func (s *Service) AddReadings(versionID string, rs []ReadingRequest) ([]*domain.Measurement, error) {
	if err := ValidateBatch(rs); err != nil {
		return nil, err
	}
	in := make([]store.BatchInput, len(rs))
	for i, r := range rs {
		var ra time.Time
		if r.RecordedAt != nil {
			ra = *r.RecordedAt
		}
		in[i] = store.BatchInput{
			Part: r.Part, Operator: r.Operator, Trial: *r.Trial,
			Value: *r.Value, RecordedAt: ra,
		}
	}
	ms, err := s.st.BatchAddMeasurement(versionID, in)
	if err != nil {
		return nil, err
	}
	_ = s.recomputeFingerprintAndStale(versionID)
	return ms, nil
}

// UpdateReading applies a value change with revision guard.
func (s *Service) UpdateReading(measurementID string, req UpdateReadingRequest) (*domain.Measurement, error) {
	rev, err := ValidateUpdate(req)
	if err != nil {
		return nil, err
	}
	var ra time.Time
	if req.RecordedAt != nil {
		ra = *req.RecordedAt
	} else {
		ra = time.Now().UTC()
	}
	m, err := s.st.UpdateMeasurement(measurementID, *rev, *req.Value, ra)
	if err != nil {
		return nil, err
	}
	_ = s.recomputeFingerprintAndStale(m.VersionID)
	return m, nil
}

// DeleteReadingFrom deletes a reading (optionally revision-guarded) and
// refreshes the version fingerprint / stale flags.
func (s *Service) DeleteReadingFrom(versionID, measurementID string, revision int64) error {
	if err := s.st.DeleteMeasurement(measurementID, revision); err != nil {
		return err
	}
	return s.recomputeFingerprintAndStale(versionID)
}

// recomputeFingerprintAndStale updates the version fingerprint after any
// data mutation and marks all results bound to older fingerprints stale.
func (s *Service) recomputeFingerprintAndStale(versionID string) error {
	ms, err := s.st.ListMeasurements(versionID)
	if err != nil {
		return err
	}
	fp := DataFingerprint(ms)
	if err := s.st.SetFingerprint(versionID, fp); err != nil {
		return err
	}
	return s.st.MarkResultsStale(versionID)
}

// ListMeasurements lists readings of a version.
func (s *Service) ListMeasurements(versionID string) ([]*domain.Measurement, error) {
	return s.st.ListMeasurements(versionID)
}

// GetMeasurement loads one reading.
func (s *Service) GetMeasurement(id string) (*domain.Measurement, error) {
	return s.st.GetMeasurement(id)
}

// Finalize freezes a version and computes & stores its result snapshot.
func (s *Service) Finalize(studyID, versionID string) (*domain.Version, *AnalysisResult, error) {
	v, err := s.GetVersion(studyID, versionID)
	if err != nil {
		return nil, nil, err
	}
	if v.Status == domain.StatusFinalized {
		return nil, nil, store.ErrFinalized
	}
	result, err := s.computeAndStore(v, true)
	if err != nil {
		return nil, nil, err
	}
	fv, err := s.st.FinalizeVersion(versionID)
	if err != nil {
		return nil, nil, err
	}
	return fv, result, nil
}

// Compute runs the analysis for a version without changing status and
// stores the result (results are append-only).
func (s *Service) Compute(studyID, versionID string) (*AnalysisResult, error) {
	v, err := s.GetVersion(studyID, versionID)
	if err != nil {
		return nil, err
	}
	return s.computeAndStore(v, false)
}

func (s *Service) computeAndStore(v *domain.Version, onFinalize bool) (*AnalysisResult, error) {
	st, err := s.st.GetStudy(v.StudyID)
	if err != nil {
		return nil, err
	}
	ms, err := s.st.ListMeasurements(v.ID)
	if err != nil {
		return nil, err
	}
	raw := make([]RawReading, len(ms))
	for i, m := range ms {
		raw[i] = RawReading{Part: m.Part, Operator: m.Operator, Trial: m.Trial, Value: m.Value}
	}
	// Ensure fingerprint is current.
	fp := DataFingerprint(ms)
	if fp != v.Fingerprint {
		_ = s.st.SetFingerprint(v.ID, fp)
		v.Fingerprint = fp
	}
	res, err := Analyze(st, v, raw)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	r := &domain.Result{
		VersionID: v.ID, StudyID: v.StudyID, Fingerprint: fp,
		Stale: false, Payload: string(payload),
	}
	if err := s.st.SaveResult(r); err != nil {
		return nil, err
	}
	res.ID = r.ID
	_ = onFinalize
	return res, nil
}

// ResultView is a stored result plus parsed payload.
type ResultView struct {
	*domain.Result
	Analysis *AnalysisResult
}

// GetResult returns a result and indicates staleness against the current
// fingerprint of its version (a result is stale when data changed after
// it was computed).
func (s *Service) GetResult(id string) (*ResultView, error) {
	r, err := s.st.GetResult(id)
	if err != nil {
		return nil, err
	}
	v, err := s.st.GetVersion(r.VersionID)
	if err == nil && v.Fingerprint != r.Fingerprint {
		r.Stale = true
	}
	var a AnalysisResult
	if err := json.Unmarshal([]byte(r.Payload), &a); err != nil {
		return nil, err
	}
	return &ResultView{Result: r, Analysis: &a}, nil
}

// ListResults returns results of a version with dynamic stale flags.
func (s *Service) ListResults(versionID string) ([]*ResultView, error) {
	rs, err := s.st.ListResults(versionID)
	if err != nil {
		return nil, err
	}
	v, _ := s.st.GetVersion(versionID)
	out := make([]*ResultView, 0, len(rs))
	for _, r := range rs {
		if v != nil && v.Fingerprint != r.Fingerprint {
			r.Stale = true
		}
		var a AnalysisResult
		if err := json.Unmarshal([]byte(r.Payload), &a); err != nil {
			return nil, err
		}
		out = append(out, &ResultView{Result: r, Analysis: &a})
	}
	return out, nil
}
