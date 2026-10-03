package service

import (
	"math"
	"path/filepath"
	"testing"

	"msagrr/domain"
	"msagrr/preset"
	"msagrr/store"
)

func tmpStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, dir
}

func f(v float64) *float64 { return &v }
func i64(v int64) *int64   { return &v }
func ti(v int) *int        { return &v }

func presetRequests() []ReadingRequest {
	ds := preset.Dataset()
	out := make([]ReadingRequest, len(ds))
	for i, r := range ds {
		out[i] = ReadingRequest{Part: r.Part, Operator: r.Operator, Trial: ti(r.Trial), Value: f(r.Value)}
	}
	return out
}

func createPopulatedStudy(t *testing.T, svc *Service) (*domain.Study, *domain.Version) {
	t.Helper()
	st, v, err := svc.CreateStudy(CreateStudyRequest{
		GaugeID: "G-001", GaugeName: "micrometer",
		Characteristic: "outer diameter", Tolerance: f(1.0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddReadings(v.ID, presetRequests()); err != nil {
		t.Fatal(err)
	}
	return st, v
}

func TestCreateAndValidateStudy(t *testing.T) {
	st, _ := tmpStore(t)
	svc := New(st)

	// tolerance must be positive
	if _, _, err := svc.CreateStudy(CreateStudyRequest{
		GaugeID: "g", Characteristic: "c", Tolerance: f(0),
	}); err == nil {
		t.Fatal("zero tolerance accepted")
	} else if ve, ok := err.(*ValidationError); !ok || ve.Fields[0].Field != "tolerance" {
		t.Fatalf("want field=tolerance error, got %v", err)
	}
	if _, _, err := svc.CreateStudy(CreateStudyRequest{
		GaugeID: "g", Characteristic: "c", Tolerance: f(-2),
	}); err == nil {
		t.Fatal("negative tolerance accepted")
	}
	if _, _, err := svc.CreateStudy(CreateStudyRequest{
		GaugeID: "", Characteristic: "c", Tolerance: f(1),
	}); err == nil {
		t.Fatal("blank gauge accepted")
	}

	study, v, err := svc.CreateStudy(CreateStudyRequest{
		GaugeID: "g", Characteristic: "length", Tolerance: f(0.5),
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.VersionNo != 1 || v.Status != domain.StatusOpen {
		t.Fatalf("initial version wrong: %+v", v)
	}
	if study.CurrentVersion != v.ID {
		t.Fatal("study current version not set")
	}
}

func TestReadingValidationFields(t *testing.T) {
	_, svc := func() (*store.Store, *Service) {
		st, _ := tmpStore(t)
		return st, New(st)
	}()
	_, v, err := svc.CreateStudy(CreateStudyRequest{GaugeID: "g", Characteristic: "c", Tolerance: f(1)})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.AddReading(v.ID, ReadingRequest{Part: "P", Operator: "A", Trial: ti(1), Value: f(math.NaN())}); err == nil {
		t.Error("NaN accepted")
	} else if ve, ok := err.(*ValidationError); !ok || ve.Fields[0].Field != "value" {
		t.Errorf("want value field error, got %v", err)
	}
	if _, err := svc.AddReading(v.ID, ReadingRequest{Part: "P", Operator: "A", Trial: ti(1), Value: f(math.Inf(1))}); err == nil {
		t.Error("+Inf accepted")
	}
	// batch errors include the index path
	_, err = svc.AddReadings(v.ID, []ReadingRequest{
		{Part: "P1", Operator: "A", Trial: ti(1), Value: f(1)},
		{Part: "P1", Operator: "A", Trial: ti(0), Value: f(1)}, // bad trial
	})
	if err == nil {
		t.Fatal("bad batch accepted")
	}
	ve := err.(*ValidationError)
	found := false
	for _, fe := range ve.Fields {
		if fe.Field == "readings[1].trial" {
			found = true
		}
	}
	if !found {
		t.Fatalf("indexed field path missing: %+v", ve.Fields)
	}
}

func TestDuplicateSlotRejected(t *testing.T) {
	st, _ := tmpStore(t)
	svc := New(st)
	_, v := createPopulatedStudy(t, svc)

	// preset already contains every P/operator/trial
	_, err := svc.AddReading(v.ID, ReadingRequest{
		Part: preset.PartLabels[0], Operator: preset.OperatorLabels[0],
		Trial: ti(1), Value: f(999),
	})
	if err != store.ErrSlotDuplicate {
		t.Fatalf("got %v, want ErrSlotDuplicate", err)
	}
}

func TestComputePresetAndDesignInfo(t *testing.T) {
	st, _ := tmpStore(t)
	svc := New(st)
	study, v := createPopulatedStudy(t, svc)

	res, err := svc.Compute(study.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Design.Parts != 10 || res.Design.Operators != 3 ||
		res.Design.Repeats != 3 || res.Design.Observations != 90 {
		t.Fatalf("design info wrong: %+v", res.Design)
	}
	if res.Data.RawCount != 90 || res.Data.UsedCount != 90 {
		t.Fatalf("data counts wrong: %+v", res.Data)
	}
	if len(res.Data.Excluded) != 0 {
		t.Fatalf("balanced data reported exclusions: %+v", res.Data.Excluded)
	}
	// hand SS from preset
	var ssPart, ssOp, ssInter, ssE, ssTotal float64
	for _, row := range res.ANOVA {
		switch row.Source {
		case "part":
			ssPart = row.SS
		case "operator":
			ssOp = row.SS
		case "part*operator (pooled into repeatability)":
			ssInter = row.SS
		case "repeatability":
			ssE = row.SS
		}
	}
	ssTotal = ssPart + ssOp + ssInter + ssE
	for _, c := range []struct{ got, want float64 }{
		{ssPart, preset.SSPart}, {ssOp, preset.SSOperator},
		{ssInter, preset.SSInteraction}, {ssE, preset.SSRepeat + preset.SSInteraction},
		{ssTotal, preset.SSTotal},
	} {
		if math.Abs(c.got-c.want) > 1e-9*math.Max(1, math.Abs(c.want)) {
			t.Errorf("SS got %v want %v", c.got, c.want)
		}
	}
	if res.Metrics.PctGRRTolerance <= 0 || res.Metrics.NDC == nil {
		t.Fatalf("metrics incomplete: %+v", res.Metrics)
	}
}

func TestFinalizeFreezesAndReturnStudyClones(t *testing.T) {
	st, _ := tmpStore(t)
	svc := New(st)
	study, v := createPopulatedStudy(t, svc)

	fv, _, err := svc.Finalize(study.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fv.Status != domain.StatusFinalized || fv.FinalizedAt.IsZero() {
		t.Fatal("version not finalized")
	}

	// every write to a finalized version is rejected
	if _, err := svc.AddReading(v.ID, ReadingRequest{
		Part: "PX", Operator: "OX", Trial: ti(1), Value: f(1),
	}); err != store.ErrFinalized {
		t.Fatalf("add on finalized: %v", err)
	}
	ms, err := svc.ListMeasurements(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateReading(ms[0].ID, UpdateReadingRequest{Value: f(123), Revision: i64(ms[0].Revision)}); err != store.ErrFinalized {
		t.Fatalf("update on finalized: %v", err)
	}
	if err := svc.DeleteReadingFrom(v.ID, ms[0].ID, 0); err != store.ErrFinalized {
		t.Fatalf("delete on finalized: %v", err)
	}
	if _, _, err := svc.Finalize(study.ID, v.ID); err != store.ErrFinalized {
		t.Fatalf("re-finalize: %v", err)
	}

	// start a return study based on the finalized version
	nv, err := svc.StartReturnStudy(study.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if nv.VersionNo != 2 || nv.Status != domain.StatusOpen {
		t.Fatalf("return version wrong: %+v", nv)
	}
	if nv.ParentVersion != v.ID {
		t.Fatal("parent link missing")
	}
	cloned, err := svc.ListMeasurements(nv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cloned) != 90 {
		t.Fatalf("cloned readings = %d, want 90", len(cloned))
	}

	// old version data and results are still available untouched
	old, err := svc.ListMeasurements(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 90 {
		t.Fatal("old version readings changed")
	}
	results, err := svc.ListResults(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("old version lost its results")
	}
	for _, r := range results {
		if r.Stale {
			t.Error("finalized version results must never be marked stale")
		}
	}

	// editing the new version does not change the old one
	if _, err := svc.UpdateReading(cloned[0].ID, UpdateReadingRequest{
		Value: f(cloned[0].Value + 5), Revision: i64(cloned[0].Revision),
	}); err != nil {
		t.Fatal(err)
	}
	old0, err := svc.GetMeasurement(old[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if old0.Value != old[0].Value {
		t.Fatal("change in new version leaked into finalized version")
	}
}

func TestResultsGoStaleNotOverwritten(t *testing.T) {
	st, _ := tmpStore(t)
	svc := New(st)
	study, v := createPopulatedStudy(t, svc)

	r1, err := svc.Compute(study.ID, v.ID)
	_ = r1
	if err != nil {
		t.Fatal(err)
	}
	listed, err := svc.ListResults(v.ID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("results after first compute: %v %d", err, len(listed))
	}
	if listed[0].Stale {
		t.Fatal("fresh result reported stale")
	}
	firstID := listed[0].ID

	// change one reading -> old result must be marked stale, not replaced
	ms, _ := svc.ListMeasurements(v.ID)
	if _, err := svc.UpdateReading(ms[0].ID, UpdateReadingRequest{
		Value: f(ms[0].Value + 1), Revision: i64(ms[0].Revision),
	}); err != nil {
		t.Fatal(err)
	}
	rv, err := svc.GetResult(firstID)
	if err != nil {
		t.Fatal(err)
	}
	if !rv.Stale {
		t.Fatal("old result not marked stale after data change")
	}
	if rv.Analysis.DataFingerprint == "" {
		t.Fatal("result missing bound fingerprint")
	}

	// recompute -> a second, distinct result exists; the stale one stays
	r2, err := svc.Compute(study.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r2.ID == firstID {
		t.Fatal("result overwritten instead of appended")
	}
	listed, _ = svc.ListResults(v.ID)
	if len(listed) != 2 {
		t.Fatalf("results = %d, want 2", len(listed))
	}
}

func TestOptimisticConcurrency(t *testing.T) {
	st, _ := tmpStore(t)
	svc := New(st)
	_, v := createPopulatedStudy(t, svc)
	ms, _ := svc.ListMeasurements(v.ID)
	m := ms[0]

	// both tablets loaded revision 1; first succeeds
	updated, err := svc.UpdateReading(m.ID, UpdateReadingRequest{Value: f(10), Revision: i64(m.Revision)})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != m.Revision+1 {
		t.Fatalf("revision = %d, want %d", updated.Revision, m.Revision+1)
	}
	// second writer still sends the stale revision -> conflict, not silent overwrite
	if _, err := svc.UpdateReading(m.ID, UpdateReadingRequest{Value: f(20), Revision: i64(m.Revision)}); err != store.ErrRevisionConflict {
		t.Fatalf("second writer: %v, want conflict", err)
	}
	// value is the first writer's, not the second's
	again, _ := svc.GetMeasurement(m.ID)
	if again.Value != 10 {
		t.Fatalf("value = %v, stale writer silently overwrote", again.Value)
	}
}

func TestRestartPersistsDataAndVersions(t *testing.T) {
	st, dir := tmpStore(t)
	svc := New(st)
	study, v := createPopulatedStudy(t, svc)
	if _, err := svc.Compute(study.ID, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Finalize(study.ID, v.ID); err != nil {
		t.Fatal(err)
	}
	nv, err := svc.StartReturnStudy(study.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// reopen the same file
	st2, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	svc2 := New(st2)
	stdy, err := svc2.GetStudy(study.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stdy.Tolerance != 1.0 {
		t.Fatal("study lost on restart")
	}
	vs, err := svc2.ListVersions(study.ID)
	if err != nil || len(vs) != 2 {
		t.Fatalf("versions after restart: %v %d", err, len(vs))
	}
	if vs[0].Status != domain.StatusFinalized {
		t.Fatal("finalized status lost")
	}
	oldMs, err := svc2.ListMeasurements(v.ID)
	if err != nil || len(oldMs) != 90 {
		t.Fatalf("old readings after restart: %v %d", err, len(oldMs))
	}
	newMs, err := svc2.ListMeasurements(nv.ID)
	if err != nil || len(newMs) != 90 {
		t.Fatalf("cloned readings after restart: %v %d", err, len(newMs))
	}
	results, err := svc2.ListResults(v.ID)
	if err != nil || len(results) != 2 {
		// one result from the explicit Compute, one from Finalize
		t.Fatalf("results after restart: %v %d", err, len(results))
	}
	for _, r := range results {
		if r.Stale {
			t.Fatal("stored result stale flag wrong after restart")
		}
	}
}

func TestFingerprintChangeOnEdit(t *testing.T) {
	st, _ := tmpStore(t)
	svc := New(st)
	_, v := createPopulatedStudy(t, svc)
	ms, _ := svc.ListMeasurements(v.ID)
	fp1 := DataFingerprint(ms)
	if len(fp1) < 20 {
		t.Fatal("fingerprint malformed")
	}
	// changing a value changes the fingerprint
	ms[0].Value += 3
	fp2 := DataFingerprint(ms)
	if fp1 == fp2 {
		t.Fatal("fingerprint unchanged after value edit")
	}
	// reordering readings leaves the fingerprint unchanged
	rev := make([]*domain.Measurement, len(ms))
	copy(rev, ms)
	rev[1], rev[5] = rev[5], rev[1]
	rev[10], rev[42] = rev[42], rev[10]
	if DataFingerprint(rev) != fp2 {
		t.Fatal("fingerprint must be order independent")
	}
	// relabel-identical content (same multiset) -> same fingerprint
	rev2 := make([]*domain.Measurement, len(ms))
	copy(rev2, ms)
	rev2[0], rev2[len(rev2)-1] = rev2[len(rev2)-1], rev2[0]
	if DataFingerprint(rev2) != fp2 {
		t.Fatal("fingerprint must be order independent")
	}
}

func TestComputeFailsWithTooFewLevels(t *testing.T) {
	st, _ := tmpStore(t)
	svc := New(st)
	study, v, err := svc.CreateStudy(CreateStudyRequest{GaugeID: "g", Characteristic: "c", Tolerance: f(1)})
	if err != nil {
		t.Fatal(err)
	}
	// only one part, two operators -> cannot estimate part variation
	_, err = svc.AddReadings(v.ID, []ReadingRequest{
		{Part: "P1", Operator: "A", Trial: ti(1), Value: f(1)},
		{Part: "P1", Operator: "A", Trial: ti(2), Value: f(2)},
		{Part: "P1", Operator: "B", Trial: ti(1), Value: f(3)},
		{Part: "P1", Operator: "B", Trial: ti(2), Value: f(4)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Compute(study.ID, v.ID); err == nil {
		t.Fatal("analysis with one part must fail")
	}
}

func TestUnbalancedAnalysisReportsUsedAndExcluded(t *testing.T) {
	st, _ := tmpStore(t)
	svc := New(st)
	study, v, err := svc.CreateStudy(CreateStudyRequest{
		GaugeID: "g", Characteristic: "c", Tolerance: f(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := presetRequests()
	// drop a handful of readings to simulate missing measurements:
	// remove P01/Carol trials 2,3 (part still seen by all -> stays, R=1
	// would drop too much; instead remove only trial 3 across cells for
	// one part, plus a few scattered third trials)
	var kept []ReadingRequest
	for _, r := range reqs {
		if r.Part == "P01" && r.Operator == "Carol" && *r.Trial == 3 {
			continue
		}
		if r.Part == "P02" && r.Operator == "Bob" && *r.Trial == 3 {
			continue
		}
		kept = append(kept, r)
	}
	if _, err := svc.AddReadings(v.ID, kept); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Compute(study.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	// R shrinks from 3 to 2 (trial 3 missing in two cells); all parts and
	// operators are retained.
	if res.Design.Repeats != 2 {
		t.Fatalf("R = %d, want 2 (missing third repeats)", res.Design.Repeats)
	}
	if res.Design.Parts != 10 || res.Design.Operators != 3 {
		t.Fatalf("design = %+v", res.Design)
	}
	if res.Data.RawCount != 88 || res.Data.UsedCount != 60 {
		t.Fatalf("counts raw=%d used=%d", res.Data.RawCount, res.Data.UsedCount)
	}
	if len(res.Data.Excluded) != 28 {
		t.Fatalf("excluded readings = %d, want 28", len(res.Data.Excluded))
	}
	if len(res.Notes) == 0 {
		t.Fatal("expected explanatory notes about exclusions")
	}

	// dropping a whole part-operator pair forces that part out
	st2, _ := tmpStore(t)
	svc2 := New(st2)
	study2, v2, _ := svc2.CreateStudy(CreateStudyRequest{GaugeID: "g2", Characteristic: "c", Tolerance: f(1)})
	var kept2 []ReadingRequest
	for _, r := range presetRequests() {
		if r.Part == "P01" && r.Operator == "Carol" {
			continue // P01 never measured by Carol
		}
		kept2 = append(kept2, r)
	}
	if _, err := svc2.AddReadings(v2.ID, kept2); err != nil {
		t.Fatal(err)
	}
	res2, err := svc2.Compute(study2.ID, v2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Design.Parts != 9 {
		t.Fatalf("parts = %d, want 9 (P01 incomplete dropped)", res2.Design.Parts)
	}
	droppedP01 := 0
	for _, e := range res2.Data.Excluded {
		if e.Part == "P01" {
			droppedP01++
		}
	}
	if droppedP01 != 6 { // Alice+Bob x 3 trials
		t.Fatalf("P01 excluded readings = %d, want 6", droppedP01)
	}
}
