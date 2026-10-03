package store

import (
	"path/filepath"
	"testing"

	"msagrr/domain"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestStudyVersionLifecycle(t *testing.T) {
	s := openTest(t)
	st, err := s.CreateStudy("g", "name", "char", 1.5)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := s.CreateVersion(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v1.VersionNo != 1 {
		t.Fatalf("version number = %d", v1.VersionNo)
	}
	st2, err := s.GetStudy(st.ID)
	if err != nil || st2.CurrentVersion != v1.ID {
		t.Fatalf("current version not persisted: %v", err)
	}

	if v1.ID == "" {
		t.Fatal("empty version id")
	}
	if _, err := s.GetVersion("does-not-exist"); err != ErrNotFound {
		t.Fatalf("missing version err = %v, want ErrNotFound", err)
	}
	got, err := s.GetVersion(v1.ID)
	if err != nil || got.ID != v1.ID {
		t.Fatalf("get version: %v %+v", err, got)
	}
}

func TestMeasurementCRUDAndUniqueSlot(t *testing.T) {
	s := openTest(t)
	st, _ := s.CreateStudy("g", "", "c", 1)
	v, _ := s.CreateVersion(st.ID)

	m, err := s.AddMeasurement(v.ID, "P1", "A", 1, 10.0, baseTime())
	if err != nil {
		t.Fatal(err)
	}
	if m.Revision != 1 {
		t.Fatalf("new measurement revision = %d", m.Revision)
	}
	if _, err := s.AddMeasurement(v.ID, "P1", "A", 1, 11.0, baseTime()); err != ErrSlotDuplicate {
		t.Fatalf("duplicate slot: %v", err)
	}

	// batch
	added, err := s.BatchAddMeasurement(v.ID, []BatchInput{
		{Part: "P1", Operator: "A", Trial: 2, Value: 10},
		{Part: "P1", Operator: "B", Trial: 1, Value: 10},
	})
	if err != nil || len(added) != 2 {
		t.Fatalf("batch: %v %d", err, len(added))
	}
	if _, err := s.BatchAddMeasurement(v.ID, []BatchInput{
		{Part: "P9", Operator: "A", Trial: 1, Value: 1},
		{Part: "P9", Operator: "A", Trial: 1, Value: 2}, // dup within batch
	}); err != ErrSlotDuplicate {
		t.Fatalf("in-batch duplicate: %v", err)
	}

	// update with revision guard
	up, err := s.UpdateMeasurement(m.ID, 1, 12.0, baseTime())
	if err != nil {
		t.Fatal(err)
	}
	if up.Value != 12 || up.Revision != 2 {
		t.Fatalf("update = %+v", up)
	}
	if _, err := s.UpdateMeasurement(m.ID, 1, 13.0, baseTime()); err != ErrRevisionConflict {
		t.Fatalf("stale revision update: %v", err)
	}

	// list ordering
	ms, err := s.ListMeasurements(v.ID)
	if err != nil || len(ms) != 3 {
		t.Fatalf("list: %v %d", err, len(ms))
	}

	// delete
	if err := s.DeleteMeasurement(m.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMeasurement(m.ID); err != ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
	// slot can be reused after delete
	if _, err := s.AddMeasurement(v.ID, "P1", "A", 1, 14.0, baseTime()); err != nil {
		t.Fatalf("slot reuse after delete: %v", err)
	}
}

func TestFinalizeRejectsWrites(t *testing.T) {
	s := openTest(t)
	st, _ := s.CreateStudy("g", "", "c", 1)
	v, _ := s.CreateVersion(st.ID)
	m, _ := s.AddMeasurement(v.ID, "P1", "A", 1, 1, baseTime())
	if _, err := s.FinalizeVersion(v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinalizeVersion(v.ID); err != ErrFinalized {
		t.Fatalf("re-finalize: %v", err)
	}
	if _, err := s.AddMeasurement(v.ID, "P1", "A", 2, 1, baseTime()); err != ErrFinalized {
		t.Fatalf("add: %v", err)
	}
	if _, err := s.UpdateMeasurement(m.ID, 1, 2, baseTime()); err != ErrFinalized {
		t.Fatalf("update: %v", err)
	}
	if err := s.DeleteMeasurement(m.ID, 0); err != ErrFinalized {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.BatchAddMeasurement(v.ID, []BatchInput{{Part: "x", Operator: "y", Trial: 1, Value: 1}}); err != ErrFinalized {
		t.Fatalf("batch: %v", err)
	}
}

func TestCloneAndResultStaleness(t *testing.T) {
	s := openTest(t)
	st, _ := s.CreateStudy("g", "", "c", 1)
	v1, _ := s.CreateVersion(st.ID)
	if _, err := s.BatchAddMeasurement(v1.ID, []BatchInput{
		{Part: "P1", Operator: "A", Trial: 1, Value: 1},
		{Part: "P1", Operator: "B", Trial: 1, Value: 2},
		{Part: "P2", Operator: "A", Trial: 1, Value: 3},
		{Part: "P2", Operator: "B", Trial: 1, Value: 4},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveResult(&domain.Result{VersionID: v1.ID, StudyID: st.ID, Fingerprint: "fp1", Payload: "{}"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinalizeVersion(v1.ID); err != nil {
		t.Fatal(err)
	}

	// cannot clone an open/nonexistent parent
	if _, err := s.CreateVersionFrom(st.ID, "nope"); err != ErrNotFound {
		t.Fatalf("clone missing parent: %v", err)
	}

	v2, err := s.CreateVersionFrom(st.ID, v1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v2.VersionNo != 2 || v2.Status != domain.StatusOpen || v2.ParentVersion != v1.ID {
		t.Fatalf("clone = %+v", v2)
	}
	ms, err := s.ListMeasurements(v2.ID)
	if err != nil || len(ms) != 4 {
		t.Fatalf("cloned readings: %v %d", err, len(ms))
	}

	// mark stale on v1 must not touch its stored payload readability
	rs1, err := s.ListResults(v1.ID)
	if err != nil || len(rs1) != 1 {
		t.Fatalf("old results: %v %d", err, len(rs1))
	}
	if err := s.MarkResultsStale(v1.ID); err != nil {
		t.Fatal(err)
	}
	rs1b, _ := s.ListResults(v1.ID)
	if !rs1b[0].Stale {
		t.Fatal("result not marked stale")
	}
}
