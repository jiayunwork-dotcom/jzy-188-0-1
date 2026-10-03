// Package store persists studies, versions, measurements and frozen
// results in an embedded SQLite database. The database file lives on a
// mounted volume so a restart preserves all data and every version.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned for unknown ids.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned on stale row versions (optimistic concurrency).
var ErrConflict = errors.New("conflict: row was modified by another client")

// ErrFinalized is returned for any write against a finalized version.
var ErrFinalized = errors.New("version is finalized and read-only")

// ErrDuplicate is returned for a repeated (part, operator, trial) triple.
var ErrDuplicate = errors.New("duplicate measurement for (part, operator, trial)")

// Study is one gage/characteristic study header.
type Study struct {
	ID             string    `json:"id"`
	GageID         string    `json:"gage_id"`
	Characteristic string    `json:"characteristic"`
	Tolerance      float64   `json:"tolerance"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Version is a draft or finalized data revision of a study.
type Version struct {
	ID          string       `json:"id"`
	StudyID     string       `json:"study_id"`
	VersionNo   int          `json:"version_no"`
	Status      string       `json:"status"` // draft | finalized
	BasedOnID   string       `json:"based_on_id"`
	CreatedAt   time.Time    `json:"created_at"`
	FinalizedAt sql.NullTime `json:"finalized_at"`
}

// Measurement is a stored reading. RowVersion implements optimistic locking.
type Measurement struct {
	ID         string    `json:"id"`
	VersionID  string    `json:"version_id"`
	PartID     string    `json:"part_id"`
	OperatorID string    `json:"operator_id"`
	Trial      int       `json:"trial"`
	Value      float64   `json:"value"`
	RecordedAt time.Time `json:"recorded_at"`
	RowVersion int       `json:"row_version"`
}

// Result is a frozen analysis output bound to a data hash.
type Result struct {
	ID        string    `json:"id"`
	VersionID string    `json:"version_id"`
	DataHash  string    `json:"data_hash"`
	Payload   string    `json:"-"`
	Stale     bool      `json:"stale"`
	CreatedAt time.Time `json:"created_at"`
}

// Store wraps the database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS studies (
    id              TEXT PRIMARY KEY,
    gage_id         TEXT NOT NULL,
    characteristic  TEXT NOT NULL,
    tolerance       REAL NOT NULL CHECK (tolerance > 0),
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS versions (
    id            TEXT PRIMARY KEY,
    study_id      TEXT NOT NULL REFERENCES studies(id),
    version_no    INTEGER NOT NULL,
    status        TEXT NOT NULL CHECK (status IN ('draft','finalized')),
    based_on_id   TEXT REFERENCES versions(id),
    created_at    TEXT NOT NULL,
    finalized_at  TEXT,
    UNIQUE (study_id, version_no)
);
CREATE TABLE IF NOT EXISTS measurements (
    id           TEXT PRIMARY KEY,
    version_id   TEXT NOT NULL REFERENCES versions(id),
    part_id      TEXT NOT NULL,
    operator_id  TEXT NOT NULL,
    trial        INTEGER NOT NULL CHECK (trial >= 1),
    value        REAL NOT NULL,
    recorded_at  TEXT NOT NULL,
    row_version  INTEGER NOT NULL DEFAULT 1,
    UNIQUE (version_id, part_id, operator_id, trial)
);
CREATE TABLE IF NOT EXISTS results (
    id           TEXT PRIMARY KEY,
    version_id   TEXT NOT NULL REFERENCES versions(id),
    data_hash    TEXT NOT NULL,
    payload      TEXT NOT NULL,
    stale        INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_meas_version ON measurements(version_id);
CREATE INDEX IF NOT EXISTS idx_results_version ON results(version_id);
CREATE INDEX IF NOT EXISTS idx_versions_study ON versions(study_id);
`

func nowTS() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// touchStudies bumps updated_at.
func (s *Store) touchStudy(ctx context.Context, tx *sql.Tx, studyID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE studies SET updated_at = ? WHERE id = ?`, nowTS(), studyID)
	return err
}

// CreateStudy inserts a study and its draft v1.
func (s *Store) CreateStudy(ctx context.Context, st *Study, versionID string) (*Version, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ts := nowTS()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO studies(id, gage_id, characteristic, tolerance, created_at, updated_at)
		 VALUES(?,?,?,?,?,?)`,
		st.ID, st.GageID, st.Characteristic, st.Tolerance, ts, ts); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO versions(id, study_id, version_no, status, based_on_id, created_at)
		 VALUES(?,? ,1,'draft',NULL,?)`,
		versionID, st.ID, ts); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	st.CreatedAt, _ = time.Parse(time.RFC3339Nano, ts)
	st.UpdatedAt = st.CreatedAt
	return s.GetVersion(ctx, versionID)
}

// GetStudy loads a study.
func (s *Store) GetStudy(ctx context.Context, id string) (*Study, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, gage_id, characteristic, tolerance, created_at, updated_at FROM studies WHERE id=?`, id)
	return scanStudy(row)
}

// ListStudies returns all studies, newest first.
func (s *Store) ListStudies(ctx context.Context, limit, offset int) ([]*Study, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, gage_id, characteristic, tolerance, created_at, updated_at
		 FROM studies ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Study
	for rows.Next() {
		st, err := scanStudy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// UpdateStudy changes editable header fields.
func (s *Store) UpdateStudy(ctx context.Context, st *Study) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE studies SET gage_id=?, characteristic=?, tolerance=?, updated_at=? WHERE id=?`,
		st.GageID, st.Characteristic, st.Tolerance, nowTS(), st.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanStudy(r rowScanner) (*Study, error) {
	var st Study
	var created, updated string
	if err := r.Scan(&st.ID, &st.GageID, &st.Characteristic, &st.Tolerance, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	st.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	st.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &st, nil
}

// GetVersion loads a version.
func (s *Store) GetVersion(ctx context.Context, id string) (*Version, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, study_id, version_no, status, COALESCE(based_on_id,''), created_at, COALESCE(finalized_at,'')
		 FROM versions WHERE id=?`, id)
	return scanVersion(row)
}

func scanVersion(r rowScanner) (*Version, error) {
	var v Version
	var created, finalized string
	var based string
	if err := r.Scan(&v.ID, &v.StudyID, &v.VersionNo, &v.Status, &based, &created, &finalized); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	v.BasedOnID = based
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if finalized != "" {
		if ft, err := time.Parse(time.RFC3339Nano, finalized); err == nil {
			v.FinalizedAt = sql.NullTime{Time: ft, Valid: true}
		}
	}
	return &v, nil
}

// ListVersions returns a study's versions newest-first.
func (s *Store) ListVersions(ctx context.Context, studyID string) ([]*Version, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, study_id, version_no, status, COALESCE(based_on_id,''), created_at, COALESCE(finalized_at,'')
		 FROM versions WHERE study_id=? ORDER BY version_no DESC`, studyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CurrentDraft returns the draft version of a study.
func (s *Store) CurrentDraft(ctx context.Context, studyID string) (*Version, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, study_id, version_no, status, COALESCE(based_on_id,''), created_at, COALESCE(finalized_at,'')
		 FROM versions WHERE study_id=? AND status='draft' ORDER BY version_no DESC LIMIT 1`, studyID)
	return scanVersion(row)
}

// requireDraftTx fails unless the version exists and is a draft.
func (s *Store) requireDraftTx(ctx context.Context, tx *sql.Tx, versionID string) (*Version, error) {
	v, err := scanVersion(tx.QueryRowContext(ctx,
		`SELECT id, study_id, version_no, status, COALESCE(based_on_id,''), created_at, COALESCE(finalized_at,'')
		 FROM versions WHERE id=?`, versionID))
	if err != nil {
		return nil, err
	}
	if v.Status == "finalized" {
		return nil, ErrFinalized
	}
	return v, nil
}

// AddMeasurement inserts one reading into a draft version.
func (s *Store) AddMeasurement(ctx context.Context, m *Measurement) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := s.requireDraftTx(ctx, tx, m.VersionID)
	if err != nil {
		return err
	}
	ts := m.RecordedAt.UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO measurements(id, version_id, part_id, operator_id, trial, value, recorded_at, row_version)
		 VALUES(?,?,?,?,?,?,?,1)`,
		m.ID, m.VersionID, m.PartID, m.OperatorID, m.Trial, m.Value, ts)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return err
	}
	if err := s.markResultsStaleTx(ctx, tx, m.VersionID); err != nil {
		return err
	}
	if err := s.touchStudy(ctx, tx, v.StudyID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	m.RowVersion = 1
	loaded, err := s.GetMeasurement(ctx, m.ID)
	if err == nil {
		*m = *loaded
	}
	return nil
}

// BatchAdd inserts many readings atomically: any failure rolls everything
// back and reports the offending index.
type BatchError struct {
	Index   int
	Field   string
	Message string
}

func (e *BatchError) Error() string {
	return fmt.Sprintf("row %d: %s %s", e.Index, e.Field, e.Message)
}

// BatchAddMeasurements bulk-inserts readings. All rows must be valid.
func (s *Store) BatchAddMeasurements(ctx context.Context, versionID string, ms []*Measurement) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := s.requireDraftTx(ctx, tx, versionID)
	if err != nil {
		return err
	}
	for i, m := range ms {
		ts := m.RecordedAt.UTC().Format(time.RFC3339Nano)
		_, err := tx.ExecContext(ctx,
			`INSERT INTO measurements(id, version_id, part_id, operator_id, trial, value, recorded_at, row_version)
			 VALUES(?,?,?,?,?,?,?,1)`,
			m.ID, versionID, m.PartID, m.OperatorID, m.Trial, m.Value, ts)
		if err != nil {
			if isUniqueViolation(err) {
				return &BatchError{Index: i, Field: "part_id/operator_id/trial", Message: "duplicate reading within this version"}
			}
			return &BatchError{Index: i, Message: err.Error()}
		}
	}
	if err := s.markResultsStaleTx(ctx, tx, versionID); err != nil {
		return err
	}
	if err := s.touchStudy(ctx, tx, v.StudyID); err != nil {
		return err
	}
	return tx.Commit()
}

// GetMeasurement loads one reading.
func (s *Store) GetMeasurement(ctx context.Context, id string) (*Measurement, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, version_id, part_id, operator_id, trial, value, recorded_at, row_version
		 FROM measurements WHERE id=?`, id)
	return scanMeasurement(row)
}

func scanMeasurement(r rowScanner) (*Measurement, error) {
	var m Measurement
	var recorded string
	if err := r.Scan(&m.ID, &m.VersionID, &m.PartID, &m.OperatorID, &m.Trial,
		&m.Value, &recorded, &m.RowVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	m.RecordedAt, _ = time.Parse(time.RFC3339Nano, recorded)
	return &m, nil
}

// ListMeasurements returns all readings of a version.
func (s *Store) ListMeasurements(ctx context.Context, versionID string) ([]*Measurement, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, version_id, part_id, operator_id, trial, value, recorded_at, row_version
		 FROM measurements WHERE version_id=? ORDER BY part_id, operator_id, trial`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Measurement
	for rows.Next() {
		m, err := scanMeasurement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateMeasurement changes a reading with optimistic locking.
// expectedRowVersion must match the stored version; otherwise ErrConflict.
func (s *Store) UpdateMeasurement(ctx context.Context, m *Measurement, expectedRowVersion int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := s.requireDraftTx(ctx, tx, m.VersionID)
	if err != nil {
		return err
	}
	ts := m.RecordedAt.UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx,
		`UPDATE measurements SET part_id=?, operator_id=?, trial=?, value=?, recorded_at=?,
		 row_version=row_version+1
		 WHERE id=? AND row_version=?`,
		m.PartID, m.OperatorID, m.Trial, m.Value, ts, m.ID, expectedRowVersion)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Either gone or stale: distinguish for a precise 404 vs 409.
		var cur int
		if err := tx.QueryRowContext(ctx,
			`SELECT row_version FROM measurements WHERE id=?`, m.ID).Scan(&cur); err != nil {
			return ErrNotFound
		}
		return ErrConflict
	}
	if err := s.markResultsStaleTx(ctx, tx, m.VersionID); err != nil {
		return err
	}
	if err := s.touchStudy(ctx, tx, v.StudyID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	loaded, err := s.GetMeasurement(ctx, m.ID)
	if err == nil {
		*m = *loaded
	}
	return nil
}

// DeleteMeasurement removes a reading (draft versions only).
func (s *Store) DeleteMeasurement(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var versionID string
	if err := tx.QueryRowContext(ctx,
		`SELECT version_id FROM measurements WHERE id=?`, id).Scan(&versionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	v, err := s.requireDraftTx(ctx, tx, versionID)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM measurements WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := s.markResultsStaleTx(ctx, tx, versionID); err != nil {
		return err
	}
	if err := s.touchStudy(ctx, tx, v.StudyID); err != nil {
		return err
	}
	return tx.Commit()
}

// FinalizeVersion freezes a version. Existing results stay valid (they were
// computed on exactly this data); new writes are rejected forever.
func (s *Store) FinalizeVersion(ctx context.Context, versionID string) (*Version, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	v, err := scanVersion(tx.QueryRowContext(ctx,
		`SELECT id, study_id, version_no, status, COALESCE(based_on_id,''), created_at, COALESCE(finalized_at,'')
		 FROM versions WHERE id=?`, versionID))
	if err != nil {
		return nil, err
	}
	if v.Status == "finalized" {
		return v, nil // idempotent
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE versions SET status='finalized', finalized_at=? WHERE id=?`, nowTS(), versionID); err != nil {
		return nil, err
	}
	if err := s.touchStudy(ctx, tx, v.StudyID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetVersion(ctx, versionID)
}

// NewVersionFromFinalized clones a finalized version's readings into a new
// draft. Returns a typed error if the base is not finalized.
func (s *Store) NewVersionFromFinalized(ctx context.Context, baseID, newVersionID string) (*Version, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	base, err := scanVersion(tx.QueryRowContext(ctx,
		`SELECT id, study_id, version_no, status, COALESCE(based_on_id,''), created_at, COALESCE(finalized_at,'')
		 FROM versions WHERE id=?`, baseID))
	if err != nil {
		return nil, err
	}
	if base.Status != "finalized" {
		return nil, fmt.Errorf("%w: new version can only be based on a finalized version", ErrConflict)
	}
	var nextNo int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version_no),0)+1 FROM versions WHERE study_id=?`, base.StudyID).Scan(&nextNo); err != nil {
		return nil, err
	}
	ts := nowTS()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO versions(id, study_id, version_no, status, based_on_id, created_at)
		 VALUES(?,?,?,'draft',?,?)`,
		newVersionID, base.StudyID, nextNo, baseID, ts); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO measurements(id, version_id, part_id, operator_id, trial, value, recorded_at, row_version)
		 SELECT lower(hex(randomblob(8))) || '-' || lower(hex(randomblob(4))), ?, part_id, operator_id, trial, value, ?, 1
		 FROM measurements WHERE version_id=?`,
		newVersionID, ts, baseID); err != nil {
		return nil, err
	}
	if err := s.touchStudy(ctx, tx, base.StudyID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetVersion(ctx, newVersionID)
}

// SaveResult stores a computed result bound to the version's data hash.
func (s *Store) SaveResult(ctx context.Context, r *Result) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO results(id, version_id, data_hash, payload, stale, created_at)
		 VALUES(?,?,?,?,0,?)`,
		r.ID, r.VersionID, r.DataHash, r.Payload, nowTS())
	return err
}

// markResultsStaleTx flags every prior result of a draft version stale after
// a data change. Finalized versions never reach this code.
func (s *Store) markResultsStaleTx(ctx context.Context, tx *sql.Tx, versionID string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE results SET stale=1 WHERE version_id=?`, versionID)
	return err
}

// ListResults returns every result ever computed on a version, newest first.
func (s *Store) ListResults(ctx context.Context, versionID string) ([]*Result, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, version_id, data_hash, payload, stale, created_at
		 FROM results WHERE version_id=? ORDER BY created_at DESC`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Result
	for rows.Next() {
		var r Result
		var stale int
		var created string
		if err := rows.Scan(&r.ID, &r.VersionID, &r.DataHash, &r.Payload, &stale, &created); err != nil {
			return nil, err
		}
		r.Stale = stale == 1
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// LatestResult returns the most recent result on a version.
func (s *Store) LatestResult(ctx context.Context, versionID string) (*Result, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, version_id, data_hash, payload, stale, created_at
		 FROM results WHERE version_id=? ORDER BY created_at DESC LIMIT 1`, versionID)
	var r Result
	var stale int
	var created string
	if err := row.Scan(&r.ID, &r.VersionID, &r.DataHash, &r.Payload, &stale, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Stale = stale == 1
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return &r, nil
}

func isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
