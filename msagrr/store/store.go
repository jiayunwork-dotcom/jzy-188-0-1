// Package store is the embedded persistence layer (bbolt). The database
// file lives under the mounted data directory so data and all versions
// survive restarts intact.
package store

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"

	"msagrr/domain"
)

var (
	bStudies       = []byte("studies")
	bVersions      = []byte("versions")
	bVersionIndex  = []byte("versions_by_study")
	bMeasurements  = []byte("measurements")
	bMeasIndex     = []byte("meas_by_version")
	bUniqueSlot    = []byte("meas_unique_slot")
	bResults       = []byte("results")
	bResultIndex   = []byte("results_by_version")
	bSeq           = []byte("seq")
	bStudyVersionN = []byte("study_version_no")
)

// Errors returned by the store.
var (
	ErrNotFound         = errors.New("not found")
	ErrFinalized        = errors.New("study version is finalized")
	ErrSlotDuplicate    = errors.New("duplicate reading for part/operator/trial")
	ErrRevisionConflict = errors.New("revision conflict")
)

// Store wraps a bbolt database.
type Store struct {
	db *bolt.DB
}

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bStudies, bVersions, bVersionIndex, bMeasurements,
			bMeasIndex, bUniqueSlot, bResults, bResultIndex, bSeq, bStudyVersionN} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the database file.
func (s *Store) Close() error { return s.db.Close() }

func itob(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func btoi(b []byte) uint64 { return binary.BigEndian.Uint64(b) }

// nextID returns an identifier like "s-000001" inside a transaction.
func nextID(tx *bolt.Tx, prefix, seqKey string) (string, uint64, error) {
	seq := tx.Bucket(bSeq)
	n, err := seq.NextSequence()
	if err != nil {
		return "", 0, err
	}
	_ = seqKey // one global monotonic sequence
	return fmt.Sprintf("%s-%06d", prefix, n), n, nil
}

// CreateStudy persists a new study.
func (s *Store) CreateStudy(gaugeID, gaugeName, characteristic string, tolerance float64) (*domain.Study, error) {
	var out *domain.Study
	err := s.db.Update(func(tx *bolt.Tx) error {
		id, _, err := nextID(tx, "s", "study")
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		st := &domain.Study{
			ID: id, GaugeID: gaugeID, GaugeName: gaugeName,
			Characteristic: characteristic, Tolerance: tolerance,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := putJSON(tx, bStudies, []byte(id), st); err != nil {
			return err
		}
		out = st
		return nil
	})
	return out, err
}

// GetStudy loads a study.
func (s *Store) GetStudy(id string) (*domain.Study, error) {
	var st domain.Study
	if err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx, bStudies, []byte(id), &st)
	}); err != nil {
		return nil, err
	}
	return &st, nil
}

// ListStudies lists all studies, newest first.
func (s *Store) ListStudies() ([]*domain.Study, error) {
	var out []*domain.Study
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bStudies).ForEach(func(_, v []byte) error {
			var st domain.Study
			if err := json.Unmarshal(v, &st); err != nil {
				return err
			}
			out = append(out, &st)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// CreateVersion creates version no. 1 for a study and points the study's
// current version at it.
func (s *Store) CreateVersion(studyID string) (*domain.Version, error) {
	var v *domain.Version
	err := s.db.Update(func(tx *bolt.Tx) error {
		var st domain.Study
		if err := getJSON(tx, bStudies, []byte(studyID), &st); err != nil {
			return err
		}
		nv, err := insertVersion(tx, studyID, "")
		if err != nil {
			return err
		}
		st.CurrentVersion = nv.ID
		st.UpdatedAt = time.Now().UTC()
		if err := putJSON(tx, bStudies, []byte(studyID), &st); err != nil {
			return err
		}
		v = nv
		return nil
	})
	return v, err
}

// CreateVersionFrom clones the parent version's readings into a new open
// version (a return study based on the finalized version).
func (s *Store) CreateVersionFrom(studyID, parentVersionID string) (*domain.Version, error) {
	var v *domain.Version
	err := s.db.Update(func(tx *bolt.Tx) error {
		var parent domain.Version
		if err := getJSON(tx, bVersions, []byte(parentVersionID), &parent); err != nil {
			return err
		}
		if parent.StudyID != studyID || parent.Status != domain.StatusFinalized {
			return ErrFinalized
		}
		nv, err := insertVersion(tx, studyID, parentVersionID)
		if err != nil {
			return err
		}
		// Clone every reading.
		prefix := []byte(parentVersionID + "\x00")
		var cloneErr error
		tx.Bucket(bMeasIndex).ForEach(func(k, _ []byte) error {
			if len(k) <= len(prefix) || string(k[:len(prefix)]) != string(prefix) {
				return nil
			}
			mid := string(k[len(prefix):])
			var m domain.Measurement
			if err := getJSON(tx, bMeasurements, []byte(mid), &m); err != nil {
				cloneErr = err
				return err
			}
			newID, _, err := nextID(tx, "m", "measurement")
			if err != nil {
				cloneErr = err
				return err
			}
			now := time.Now().UTC()
			nm := domain.Measurement{
				ID: newID, VersionID: nv.ID, Part: m.Part, Operator: m.Operator,
				Trial: m.Trial, Value: m.Value, RecordedAt: m.RecordedAt,
				CreatedAt: now, UpdatedAt: now, Revision: 1,
			}
			if err := putJSON(tx, bMeasurements, []byte(newID), &nm); err != nil {
				cloneErr = err
				return err
			}
			if err := tx.Bucket(bMeasIndex).Put(slotKey(nv.ID, newID), []byte("1")); err != nil {
				cloneErr = err
				return err
			}
			if err := tx.Bucket(bUniqueSlot).Put(uniqueKey(nv.ID, m.Part, m.Operator, m.Trial), []byte(newID)); err != nil {
				cloneErr = err
				return err
			}
			return nil
		})
		if cloneErr != nil {
			return cloneErr
		}
		v = nv
		return nil
	})
	return v, err
}

func insertVersion(tx *bolt.Tx, studyID, parent string) (*domain.Version, error) {
	b := tx.Bucket(bStudyVersionN)
	var no uint64 = 1
	if cur := b.Get([]byte(studyID)); cur != nil {
		no = btoi(cur) + 1
	}
	if err := b.Put([]byte(studyID), itob(no)); err != nil {
		return nil, err
	}
	id, _, err := nextID(tx, "v", "version")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	v := &domain.Version{
		ID: id, StudyID: studyID, VersionNo: int(no),
		Status: domain.StatusOpen, Revision: 1, CreatedAt: now,
		ParentVersion: parent,
	}
	if err := putJSON(tx, bVersions, []byte(id), v); err != nil {
		return nil, err
	}
	if err := tx.Bucket(bVersionIndex).Put(versionIndexKey(studyID, no), []byte(id)); err != nil {
		return nil, err
	}
	return v, nil
}

func versionIndexKey(studyID string, no uint64) []byte {
	return []byte(fmt.Sprintf("%s\x00%012d", studyID, no))
}

func slotKey(versionID, measurementID string) []byte {
	return []byte(versionID + "\x00" + measurementID)
}

func uniqueKey(versionID, part, operator string, trial int) []byte {
	return []byte(fmt.Sprintf("%s\x00%s\x00%s\x00%06d", versionID, part, operator, trial))
}

// GetVersion loads a version.
func (s *Store) GetVersion(id string) (*domain.Version, error) {
	var v domain.Version
	if err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx, bVersions, []byte(id), &v)
	}); err != nil {
		return nil, err
	}
	return &v, nil
}

// ListVersions returns all versions of a study ordered by version no.
func (s *Store) ListVersions(studyID string) ([]*domain.Version, error) {
	var out []*domain.Version
	err := s.db.View(func(tx *bolt.Tx) error {
		prefix := []byte(studyID + "\x00")
		c := tx.Bucket(bVersionIndex).Cursor()
		for k, vid := c.Seek(prefix); k != nil && prefixMatch(k, prefix); k, vid = c.Next() {
			var v domain.Version
			if err := getJSON(tx, bVersions, vid, &v); err != nil {
				return err
			}
			out = append(out, &v)
		}
		return nil
	})
	return out, err
}

// AddMeasurement appends a reading to an open version. Rejects duplicate
// (part, operator, trial) slots and writes to finalized versions.
func (s *Store) AddMeasurement(versionID, part, operator string, trial int, value float64, recordedAt time.Time) (*domain.Measurement, error) {
	var m *domain.Measurement
	err := s.db.Update(func(tx *bolt.Tx) error {
		var v domain.Version
		if err := getJSON(tx, bVersions, []byte(versionID), &v); err != nil {
			return err
		}
		if v.Status == domain.StatusFinalized {
			return ErrFinalized
		}
		uk := uniqueKey(versionID, part, operator, trial)
		if tx.Bucket(bUniqueSlot).Get(uk) != nil {
			return ErrSlotDuplicate
		}
		id, _, err := nextID(tx, "m", "measurement")
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if recordedAt.IsZero() {
			recordedAt = now
		}
		nm := &domain.Measurement{
			ID: id, VersionID: versionID, Part: part, Operator: operator,
			Trial: trial, Value: value, RecordedAt: recordedAt,
			CreatedAt: now, UpdatedAt: now, Revision: 1,
		}
		if err := putJSON(tx, bMeasurements, []byte(id), nm); err != nil {
			return err
		}
		if err := tx.Bucket(bMeasIndex).Put(slotKey(versionID, id), []byte("1")); err != nil {
			return err
		}
		if err := tx.Bucket(bUniqueSlot).Put(uk, []byte(id)); err != nil {
			return err
		}
		v.Revision++
		if err := putJSON(tx, bVersions, []byte(versionID), &v); err != nil {
			return err
		}
		m = nm
		return nil
	})
	return m, err
}

// BatchAdd inserts many readings atomically.
type BatchInput struct {
	Part       string
	Operator   string
	Trial      int
	Value      float64
	RecordedAt time.Time
}

// BatchAddMeasurement adds multiple readings in one transaction.
func (s *Store) BatchAddMeasurement(versionID string, in []BatchInput) (added []*domain.Measurement, err error) {
	err = s.db.Update(func(tx *bolt.Tx) error {
		var v domain.Version
		if err := getJSON(tx, bVersions, []byte(versionID), &v); err != nil {
			return err
		}
		if v.Status == domain.StatusFinalized {
			return ErrFinalized
		}
		// Reject duplicates both within the batch and against storage.
		seen := map[string]bool{}
		for _, x := range in {
			uk := string(uniqueKey(versionID, x.Part, x.Operator, x.Trial))
			if seen[uk] || tx.Bucket(bUniqueSlot).Get([]byte(uk)) != nil {
				return ErrSlotDuplicate
			}
			seen[uk] = true
		}
		for _, x := range in {
			id, _, err := nextID(tx, "m", "measurement")
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			ra := x.RecordedAt
			if ra.IsZero() {
				ra = now
			}
			m := &domain.Measurement{
				ID: id, VersionID: versionID, Part: x.Part, Operator: x.Operator,
				Trial: x.Trial, Value: x.Value, RecordedAt: ra,
				CreatedAt: now, UpdatedAt: now, Revision: 1,
			}
			if err := putJSON(tx, bMeasurements, []byte(id), m); err != nil {
				return err
			}
			if err := tx.Bucket(bMeasIndex).Put(slotKey(versionID, id), []byte("1")); err != nil {
				return err
			}
			if err := tx.Bucket(bUniqueSlot).Put(uniqueKey(versionID, x.Part, x.Operator, x.Trial), []byte(id)); err != nil {
				return err
			}
			added = append(added, m)
		}
		v.Revision += int64(len(in))
		return putJSON(tx, bVersions, []byte(versionID), &v)
	})
	return added, err
}

// UpdateMeasurement changes one reading, guarded by expectedRevision.
// Two tablets editing the same value: the second writer sends a stale
// revision and gets ErrRevisionConflict instead of a silent overwrite.
func (s *Store) UpdateMeasurement(measurementID string, expectedRevision int64, value float64, recordedAt time.Time) (*domain.Measurement, error) {
	var out *domain.Measurement
	err := s.db.Update(func(tx *bolt.Tx) error {
		var m domain.Measurement
		if err := getJSON(tx, bMeasurements, []byte(measurementID), &m); err != nil {
			return err
		}
		var v domain.Version
		if err := getJSON(tx, bVersions, []byte(m.VersionID), &v); err != nil {
			return err
		}
		if v.Status == domain.StatusFinalized {
			return ErrFinalized
		}
		if m.Revision != expectedRevision {
			return ErrRevisionConflict
		}
		m.Value = value
		m.RecordedAt = recordedAt
		m.Revision++
		m.UpdatedAt = time.Now().UTC()
		if err := putJSON(tx, bMeasurements, []byte(measurementID), &m); err != nil {
			return err
		}
		v.Revision++
		if err := putJSON(tx, bVersions, []byte(m.VersionID), &v); err != nil {
			return err
		}
		out = &m
		return nil
	})
	return out, err
}

// DeleteMeasurement removes one reading from an open version.
func (s *Store) DeleteMeasurement(measurementID string, expectedRevision int64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var m domain.Measurement
		if err := getJSON(tx, bMeasurements, []byte(measurementID), &m); err != nil {
			return err
		}
		var v domain.Version
		if err := getJSON(tx, bVersions, []byte(m.VersionID), &v); err != nil {
			return err
		}
		if v.Status == domain.StatusFinalized {
			return ErrFinalized
		}
		if expectedRevision != 0 && m.Revision != expectedRevision {
			return ErrRevisionConflict
		}
		if err := tx.Bucket(bMeasurements).Delete([]byte(measurementID)); err != nil {
			return err
		}
		if err := tx.Bucket(bMeasIndex).Delete(slotKey(m.VersionID, measurementID)); err != nil {
			return err
		}
		if err := tx.Bucket(bUniqueSlot).Delete(uniqueKey(m.VersionID, m.Part, m.Operator, m.Trial)); err != nil {
			return err
		}
		v.Revision++
		return putJSON(tx, bVersions, []byte(m.VersionID), &v)
	})
}

// GetMeasurement loads a single reading.
func (s *Store) GetMeasurement(id string) (*domain.Measurement, error) {
	var m domain.Measurement
	if err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx, bMeasurements, []byte(id), &m)
	}); err != nil {
		return nil, err
	}
	return &m, nil
}

// UpdateStudy persists study header changes.
func (s *Store) UpdateStudy(st *domain.Study) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx, bStudies, []byte(st.ID), st)
	})
}

// ListMeasurements returns all readings of a version ordered by
// part, operator, trial.
func (s *Store) ListMeasurements(versionID string) ([]*domain.Measurement, error) {
	var out []*domain.Measurement
	err := s.db.View(func(tx *bolt.Tx) error {
		prefix := []byte(versionID + "\x00")
		c := tx.Bucket(bMeasIndex).Cursor()
		for k, _ := c.Seek(prefix); k != nil && prefixMatch(k, prefix); k, _ = c.Next() {
			mid := string(k[len(prefix):])
			var m domain.Measurement
			if err := getJSON(tx, bMeasurements, []byte(mid), &m); err != nil {
				return err
			}
			out = append(out, &m)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Part != out[b].Part {
			return out[a].Part < out[b].Part
		}
		if out[a].Operator != out[b].Operator {
			return out[a].Operator < out[b].Operator
		}
		return out[a].Trial < out[b].Trial
	})
	return out, nil
}

// FinalizeVersion freezes a version.
func (s *Store) FinalizeVersion(versionID string) (*domain.Version, error) {
	var out *domain.Version
	err := s.db.Update(func(tx *bolt.Tx) error {
		var v domain.Version
		if err := getJSON(tx, bVersions, []byte(versionID), &v); err != nil {
			return err
		}
		if v.Status == domain.StatusFinalized {
			return ErrFinalized
		}
		v.Status = domain.StatusFinalized
		v.FinalizedAt = time.Now().UTC()
		v.Revision++
		if err := putJSON(tx, bVersions, []byte(versionID), &v); err != nil {
			return err
		}
		out = &v
		return nil
	})
	return out, err
}

// SaveResult persists a computed result and indexes it by version.
// Results are append-only: changing data creates a new result instead.
func (s *Store) SaveResult(r *domain.Result) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if r.ID == "" {
			id, _, err := nextID(tx, "r", "result")
			if err != nil {
				return err
			}
			r.ID = id
		}
		if r.CreatedAt.IsZero() {
			r.CreatedAt = time.Now().UTC()
		}
		if err := putJSON(tx, bResults, []byte(r.ID), r); err != nil {
			return err
		}
		return tx.Bucket(bResultIndex).Put(resultIndexKey(r.VersionID, r.ID), []byte(r.ID))
	})
}

func resultIndexKey(versionID, resultID string) []byte {
	return []byte(versionID + "\x00" + resultID)
}

// GetResult loads a result.
func (s *Store) GetResult(id string) (*domain.Result, error) {
	var r domain.Result
	err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx, bResults, []byte(id), &r)
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListResults returns results of a version, oldest first.
func (s *Store) ListResults(versionID string) ([]*domain.Result, error) {
	var out []*domain.Result
	err := s.db.View(func(tx *bolt.Tx) error {
		prefix := []byte(versionID + "\x00")
		c := tx.Bucket(bResultIndex).Cursor()
		for k, _ := c.Seek(prefix); k != nil && prefixMatch(k, prefix); k, _ = c.Next() {
			rid := string(k[len(prefix):])
			var r domain.Result
			if err := getJSON(tx, bResults, []byte(rid), &r); err != nil {
				return err
			}
			out = append(out, &r)
		}
		return nil
	})
	return out, err
}

// MarkResultsStale marks every result of a version stale. Called from
// service after a data mutation; results are never overwritten.
func (s *Store) MarkResultsStale(versionID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		prefix := []byte(versionID + "\x00")
		c := tx.Bucket(bResultIndex).Cursor()
		for k, _ := c.Seek(prefix); k != nil && prefixMatch(k, prefix); k, _ = c.Next() {
			rid := k[len(prefix):]
			var r domain.Result
			if err := getJSON(tx, bResults, rid, &r); err != nil {
				return err
			}
			if !r.Stale {
				r.Stale = true
				if err := putJSON(tx, bResults, rid, &r); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// CurrentVersionFingerprint returns revision/fingerprint view helpers via
// GetVersion; fingerprint is maintained by the service.

// SetFingerprint stores the data fingerprint on a version.
func (s *Store) SetFingerprint(versionID, fingerprint string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var v domain.Version
		if err := getJSON(tx, bVersions, []byte(versionID), &v); err != nil {
			return err
		}
		v.Fingerprint = fingerprint
		return putJSON(tx, bVersions, []byte(versionID), &v)
	})
}

func putJSON(tx *bolt.Tx, bucket, key []byte, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return tx.Bucket(bucket).Put(key, data)
}

func getJSON(tx *bolt.Tx, bucket, key []byte, v interface{}) error {
	data := tx.Bucket(bucket).Get(key)
	if data == nil {
		return ErrNotFound
	}
	return json.Unmarshal(data, v)
}

func prefixMatch(k, prefix []byte) bool {
	return len(k) >= len(prefix) && string(k[:len(prefix)]) == string(prefix)
}
