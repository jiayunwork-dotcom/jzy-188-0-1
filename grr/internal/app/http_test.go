package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/automotive/grr/internal/app"
	"github.com/automotive/grr/internal/store"
	"github.com/gin-gonic/gin"
)

type server struct {
	t   *testing.T
	svc *app.Service
	st  *store.Store
	eng *gin.Engine
}

func newServer(t *testing.T, dir string) *server {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(dir, "grr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := app.New(st)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	app.NewHandler(svc).Register(r)
	return &server{t: t, svc: svc, st: st, eng: r}
}

func (s *server) do(method, path string, body any, headers map[string]string) (int, map[string]any) {
	s.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.eng.ServeHTTP(w, req)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

func mustCreateStudy(s *server) (studyID, draftID string) {
	code, body := s.do("POST", "/api/v1/studies", map[string]any{
		"gage_id": "CAL-042", "characteristic": "bore diameter", "tolerance": 0.1,
	}, nil)
	if code != http.StatusCreated {
		s.t.Fatalf("create study: %d %v", code, body)
	}
	studyID = body["study"].(map[string]any)["id"].(string)
	draftID = body["version"].(map[string]any)["id"].(string)
	return
}

func balancedPayload() map[string]any {
	type reading struct {
		PartID, OperatorID string
		Trial              int
		Value              float64
	}
	var rs []map[string]any
	// Simple 3 part x 3 op x 3 trial dataset.
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				rs = append(rs, map[string]any{
					"part_id":     p(i),
					"operator_id": op(j),
					"trial":       k + 1,
					"value":       10 + float64(i) + 0.1*float64(j) + 0.05*float64(k+i*j),
				})
			}
		}
	}
	return map[string]any{"readings": rs}
}

func p(i int) string  { return []string{"A", "B", "C"}[i] }
func op(j int) string { return []string{"Ana", "Bob", "Cy"}[j] }

func TestFullLifecycle(t *testing.T) {
	s := newServer(t, t.TempDir())
	studyID, draftID := mustCreateStudy(s)

	// Import.
	code, body := s.do("POST", "/api/v1/versions/"+draftID+"/measurements/import", balancedPayload(), nil)
	if code != http.StatusCreated {
		t.Fatalf("import: %d %v", code, body)
	}
	if body["imported"].(float64) != 27 {
		t.Fatalf("imported = %v", body["imported"])
	}

	// Duplicate import rejected wholesale (422 with the offending rows).
	code, body = s.do("POST", "/api/v1/versions/"+draftID+"/measurements/import", balancedPayload(), nil)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("dup import code = %d", code)
	}

	// In-batch duplicate is a 422 pointing at the row.
	bad := map[string]any{"readings": []map[string]any{
		{"part_id": "A", "operator_id": "Ana", "trial": 9, "value": 1},
		{"part_id": "A", "operator_id": "Ana", "trial": 9, "value": 1},
	}}
	code, body = s.do("POST", "/api/v1/versions/"+draftID+"/measurements/import", bad, nil)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("in-batch dup = %d %v", code, body)
	}

	// Compute and inspect.
	code, body = s.do("POST", "/api/v1/versions/"+draftID+"/compute", map[string]any{"method": "henderson"}, nil)
	if code != http.StatusCreated {
		t.Fatalf("compute: %d %v", code, body)
	}
	res := body["result"].(map[string]any)
	r1 := res["result"].(map[string]any)
	if r1["ss_partition_check"].(map[string]any)["within_1e_9"] != true {
		t.Fatalf("SS check failed: %v", r1["ss_partition_check"])
	}
	resultID := res["id"].(string)
	hash1 := res["data_hash"].(string)

	// Results listing shows it fresh.
	code, body = s.do("GET", "/api/v1/versions/"+draftID+"/results", nil, nil)
	if code != 200 || len(body["results"].([]any)) != 1 {
		t.Fatalf("results list %d %v", code, body)
	}

	// Fetch a measurement id, then run two updates: stale second one fails.
	_, body = s.do("GET", "/api/v1/versions/"+draftID+"/measurements", nil, nil)
	ms := body["measurements"].([]any)
	first := ms[0].(map[string]any)
	mid := first["id"].(string)
	origValue := first["value"].(float64)

	upd := func(ver int) (int, map[string]any) {
		return s.do("PUT", "/api/v1/versions/"+draftID+"/measurements/"+mid, map[string]any{
			"part_id": first["part_id"], "operator_id": first["operator_id"],
			"trial": int(first["trial"].(float64)), "value": origValue + 1.0,
			"expected_row_version": ver,
		}, nil)
	}
	code, body = upd(1)
	if code != http.StatusOK {
		t.Fatalf("first update: %d %v", code, body)
	}
	newRV := int(body["measurement"].(map[string]any)["row_version"].(float64))
	if newRV != 2 {
		t.Fatalf("row version = %d", newRV)
	}
	code, _ = upd(1)
	if code != http.StatusConflict {
		t.Fatalf("stale update code = %d, want 409", code)
	}

	// The previous result is now marked stale; recomputing yields a new hash.
	code, body = s.do("GET", "/api/v1/versions/"+draftID+"/results", nil, nil)
	results := body["results"].([]any)
	if len(results) != 1 || !results[0].(map[string]any)["stale"].(bool) {
		t.Fatalf("old result must be stale: %v", results)
	}
	code, body = s.do("POST", "/api/v1/versions/"+draftID+"/compute", map[string]any{}, nil)
	if code != http.StatusCreated {
		t.Fatalf("recompute: %d", code)
	}
	hash2 := body["result"].(map[string]any)["data_hash"].(string)
	if hash2 == hash1 {
		t.Fatal("data hash must change after edit")
	}
	// Both result records remain queryable (old one stale).
	_, body = s.do("GET", "/api/v1/versions/"+draftID+"/results", nil, nil)
	if len(body["results"].([]any)) != 2 {
		t.Fatalf("expected 2 result records")
	}
	_ = resultID

	// Validation errors point at fields.
	code, body = s.do("POST", "/api/v1/versions/"+draftID+"/measurements", map[string]any{
		"part_id": "Z", "operator_id": "Q", "trial": 1, "value": "not-a-number",
	}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("bad json = %d", code)
	}
	code, body = s.do("POST", "/api/v1/versions/"+draftID+"/measurements", map[string]any{
		"part_id": "Z", "operator_id": "Q", "trial": 0, "value": 1,
	}, nil)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("bad reading = %d", code)
	}
	fields := body["error"].(map[string]any)["fields"].([]any)
	found := false
	for _, f := range fields {
		if f.(map[string]any)["field"] == "trial" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected field=trial error, got %v", fields)
	}

	// Bad tolerance at study creation.
	code, body = s.do("POST", "/api/v1/studies", map[string]any{
		"gage_id": "X", "characteristic": "y", "tolerance": -1,
	}, nil)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("bad tolerance = %d", code)
	}

	// Finalize.
	code, body = s.do("POST", "/api/v1/versions/"+draftID+"/finalize", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("finalize: %d %v", code, body)
	}
	// All writes rejected on finalized version.
	for _, tc := range []struct {
		method, path string
		payload      any
	}{
		{"POST", "/api/v1/versions/" + draftID + "/measurements", map[string]any{
			"part_id": "A", "operator_id": "Ana", "trial": 8, "value": 1}},
		{"POST", "/api/v1/versions/" + draftID + "/measurements/import", map[string]any{"readings": []any{
			map[string]any{"part_id": "A", "operator_id": "Ana", "trial": 8, "value": 1}}}},
		{"PUT", "/api/v1/versions/" + draftID + "/measurements/" + mid, map[string]any{
			"part_id": "A", "operator_id": "Ana", "trial": 1, "value": 2,
			"expected_row_version": 2}},
		{"DELETE", "/api/v1/versions/" + draftID + "/measurements/" + mid, nil},
	} {
		code, body = s.do(tc.method, tc.path, tc.payload, nil)
		if code != http.StatusConflict {
			t.Fatalf("%s %s on finalized = %d %v", tc.method, tc.path, code, body)
		}
	}

	// Compute on finalized is allowed and is a read-only snapshot.
	code, body = s.do("POST", "/api/v1/versions/"+draftID+"/compute", nil, nil)
	if code != http.StatusCreated {
		t.Fatalf("compute finalized: %d %v", code, body)
	}

	// Open new version from finalized; data copied; old version unchanged.
	code, body = s.do("POST", "/api/v1/studies/"+studyID+"/versions", nil, nil)
	if code != http.StatusCreated {
		t.Fatalf("new version: %d %v", code, body)
	}
	v2 := body["version"].(map[string]any)
	v2ID := v2["id"].(string)
	if v2["status"] != "draft" {
		t.Fatalf("new version status %v", v2["status"])
	}
	code, body = s.do("GET", "/api/v1/versions/"+v2ID+"/measurements", nil, nil)
	if len(body["measurements"].([]any)) != 27 {
		t.Fatalf("cloned measurements = %v", body)
	}
	code, body = s.do("GET", "/api/v1/versions/"+draftID, nil, nil)
	if body["version"].(map[string]any)["status"] != "finalized" {
		t.Fatal("old version must stay finalized")
	}
	code, body = s.do("GET", "/api/v1/studies/"+studyID+"/versions", nil, nil)
	if len(body["versions"].([]any)) != 2 {
		t.Fatalf("versions = %v", body)
	}
	// Cannot create a version from a draft directly.
	code, body = s.do("POST", "/api/v1/studies/"+studyID+"/versions", map[string]any{
		"based_on_id": v2ID,
	}, nil)
	if code != http.StatusConflict {
		t.Fatalf("version from draft = %d %v", code, body)
	}
}

func TestUnbalancedComputeAccepted(t *testing.T) {
	s := newServer(t, t.TempDir())
	_, draftID := mustCreateStudy(s)
	// 4 parts, 3 ops, part D missing an operator; varying trials.
	readings := []map[string]any{}
	for i := 0; i < 4; i++ {
		for j := 0; j < 3; j++ {
			n := 3
			if i == 3 && j == 2 {
				n = 1
			}
			if i == 3 && j == 1 {
				continue // whole cell missing
			}
			for k := 0; k < n; k++ {
				readings = append(readings, map[string]any{
					"part_id": p(i % 3), "operator_id": op(j),
					"trial": k + 1, "value": float64(i) + 0.1*float64(j) + 0.01*float64(k),
				})
			}
		}
	}
	// Keep 4 distinct parts though (fix labels): use distinct names manually.
	readings = readings[:0]
	partNames := []string{"A", "B", "C", "D"}
	for i := 0; i < 4; i++ {
		for j := 0; j < 3; j++ {
			n := 3
			if i == 3 {
				if j == 1 {
					continue
				}
				if j == 2 {
					n = 1
				}
			}
			for k := 0; k < n; k++ {
				readings = append(readings, map[string]any{
					"part_id": partNames[i], "operator_id": op(j),
					"trial": k + 1, "value": float64(i) + 0.1*float64(j) + 0.01*float64(k),
				})
			}
		}
	}
	code, body := s.do("POST", "/api/v1/versions/"+draftID+"/measurements/import",
		map[string]any{"readings": readings}, nil)
	if code != http.StatusCreated {
		t.Fatalf("import: %d %v", code, body)
	}
	code, body = s.do("POST", "/api/v1/versions/"+draftID+"/compute", nil, nil)
	if code != http.StatusCreated {
		t.Fatalf("compute unbalanced: %d %v", code, body)
	}
	r := body["result"].(map[string]any)["result"].(map[string]any)
	if r["ss_partition_check"].(map[string]any)["within_1e_9"] != true {
		t.Fatalf("SS check: %v", r["ss_partition_check"])
	}
	usage := r["data_usage"].(map[string]any)
	if usage["balanced"] != false {
		t.Fatal("should report unbalanced")
	}
	if usage["used_readings"].(float64) != float64(len(readings)) {
		t.Fatalf("used %v want %d", usage["used_readings"], len(readings))
	}
	// balanced_drop still works and documents removed readings.
	code, body = s.do("POST", "/api/v1/versions/"+draftID+"/compute",
		map[string]any{"method": "balanced_drop"}, nil)
	if code != http.StatusCreated {
		t.Fatalf("balanced_drop: %d %v", code, body)
	}
	usage = body["result"].(map[string]any)["result"].(map[string]any)["data_usage"].(map[string]any)
	if usage["balanced"] != true {
		t.Fatal("drop route should report balanced")
	}
	if len(usage["removed_readings"].([]any)) == 0 {
		t.Fatal("removed readings must be listed")
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s1 := newServer(t, dir)
	studyID, draftID := mustCreateStudy(s1)
	if _, body := s1.do("POST", "/api/v1/versions/"+draftID+"/measurements/import", balancedPayload(), nil); body == nil {
		t.Fatal("import failed")
	}
	s1.do("POST", "/api/v1/versions/"+draftID+"/compute", nil, nil)
	s1.do("POST", "/api/v1/versions/"+draftID+"/finalize", nil, nil)
	s1.do("POST", "/api/v1/studies/"+studyID+"/versions", nil, nil)
	if err := s1.st.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen.
	s2 := newServer(t, dir)
	_, body := s2.do("GET", "/api/v1/studies/"+studyID, nil, nil)
	if body["study"] == nil {
		t.Fatal("study lost across restart")
	}
	_, body = s2.do("GET", "/api/v1/studies/"+studyID+"/versions", nil, nil)
	vs := body["versions"].([]any)
	if len(vs) != 2 {
		t.Fatalf("versions after restart = %d", len(vs))
	}
	// v1 still finalized and still rejects writes; results survive.
	v1 := vs[1].(map[string]any)
	if v1["status"] != "finalized" {
		t.Fatalf("v1 status = %v", v1["status"])
	}
	v1ID := v1["id"].(string)
	_, body = s2.do("GET", "/api/v1/versions/"+v1ID+"/results", nil, nil)
	if len(body["results"].([]any)) < 1 {
		t.Fatal("results lost across restart")
	}
	code, _ := s2.do("POST", "/api/v1/versions/"+v1ID+"/measurements", map[string]any{
		"part_id": "A", "operator_id": "Ana", "trial": 7, "value": 1,
	}, nil)
	if code != http.StatusConflict {
		t.Fatalf("write after restart on finalized = %d", code)
	}
}
