package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"msagrr/preset"
	"msagrr/service"
	"msagrr/store"
)

func init() { gin.SetMode(gin.TestMode) }

type env struct {
	t   *testing.T
	r   *gin.Engine
	st  *store.Store
	svc *service.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := service.New(st)
	r := gin.New()
	New(svc).Register(r)
	return &env{t: t, r: r, st: st, svc: svc}
}

func (e *env) do(method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rdr *bytes.Reader
	switch b := body.(type) {
	case nil:
		rdr = bytes.NewReader(nil)
	case string:
		rdr = bytes.NewReader([]byte(b))
	case []byte:
		rdr = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w
}

func (e *env) mustJSON(w *httptest.ResponseRecorder) map[string]any {
	e.t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		e.t.Fatalf("non-JSON response status=%d body=%s", w.Code, w.Body.String())
	}
	return out
}

func (e *env) createStudy(tolerance float64) (string, string) {
	e.t.Helper()
	w := e.do("POST", "/api/v1/studies", map[string]any{
		"gauge_id": "G1", "characteristic": "diameter", "tolerance": tolerance,
	}, nil)
	if w.Code != http.StatusCreated {
		e.t.Fatalf("create study status=%d body=%s", w.Code, w.Body.String())
	}
	out := e.mustJSON(w)
	stdy := out["study"].(map[string]any)
	v := out["version"].(map[string]any)
	return stdy["id"].(string), v["id"].(string)
}

func TestHealth(t *testing.T) {
	e := newEnv(t)
	w := e.do("GET", "/health", nil, nil)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestStudyValidationHTTP(t *testing.T) {
	e := newEnv(t)
	// tolerance zero -> 400 field error
	w := e.do("POST", "/api/v1/studies", map[string]any{
		"gauge_id": "G", "characteristic": "c", "tolerance": 0,
	}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
	out := e.mustJSON(w)
	errObj := out["error"].(map[string]any)
	fes := errObj["field_errors"].([]any)
	first := fes[0].(map[string]any)
	if first["field"] != "tolerance" {
		t.Fatalf("field=%v", first["field"])
	}

	// non-finite tolerance via loose tokens
	w = e.do("POST", "/api/v1/studies", `{"gauge_id":"G","characteristic":"c","tolerance":Infinity}`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("Infinity tolerance status=%d body=%s", w.Code, w.Body.String())
	}
	out = e.mustJSON(w)
	errObj = out["error"].(map[string]any)
	if fes, ok := errObj["field_errors"].([]any); !ok {
		t.Fatalf("missing field errors: %s", w.Body.String())
	} else {
		field := fes[0].(map[string]any)["field"]
		if field != "tolerance" {
			t.Fatalf("field=%v want tolerance", field)
		}
	}

	// malformed JSON -> 400
	w = e.do("POST", "/api/v1/studies", `{"gauge_id":`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed json status=%d", w.Code)
	}
}

// uploadPresetReal posts the 90 preset readings to the given version.
func (e *env) uploadPresetReal(studyID, versionID string) {
	e.t.Helper()
	ds := preset.Dataset()
	rs := make([]map[string]any, len(ds))
	for i, x := range ds {
		rs[i] = map[string]any{"part": x.Part, "operator": x.Operator, "trial": x.Trial, "value": x.Value}
	}
	w := e.do("POST",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/measurements/batch", studyID, versionID),
		map[string]any{"readings": rs}, nil)
	if w.Code != http.StatusCreated {
		e.t.Fatalf("batch status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestFullWorkflowHTTP(t *testing.T) {
	e := newEnv(t)
	sid, vid := e.createStudy(1.0)
	e.uploadPresetReal(sid, vid)

	// list measurements
	w := e.do("GET",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/measurements", sid, vid), nil, nil)
	if w.Code != 200 {
		t.Fatalf("list status=%d", w.Code)
	}
	if len(e.mustJSON(w)["measurements"].([]any)) != 90 {
		t.Fatal("expected 90 measurements")
	}

	// duplicate slot -> 409 with code
	w = e.do("POST",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/measurements", sid, vid),
		map[string]any{"part": preset.PartLabels[0], "operator": preset.OperatorLabels[0], "trial": 1, "value": 1}, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate status=%d", w.Code)
	}
	if code := e.mustJSON(w)["error"].(map[string]any)["code"]; code != "duplicate_slot" {
		t.Fatalf("code=%v", code)
	}

	// compute result
	w = e.do("POST",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/results", sid, vid), nil, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("compute status=%d body=%s", w.Code, w.Body.String())
	}
	res1 := e.mustJSON(w)["result"].(map[string]any)
	rid1 := res1["id"].(string)

	// finalize
	w = e.do("POST",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/finalize", sid, vid), nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("finalize status=%d body=%s", w.Code, w.Body.String())
	}

	// writes to a finalized version are rejected
	w = e.do("POST",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/measurements", sid, vid),
		map[string]any{"part": "X", "operator": "Y", "trial": 1, "value": 1}, nil)
	if w.Code != http.StatusConflict || e.mustJSON(w)["error"].(map[string]any)["code"] != "version_finalized" {
		t.Fatalf("finalized write status=%d body=%s", w.Code, w.Body.String())
	}

	// old result is still retrievable
	w = e.do("GET", "/api/v1/results/"+rid1, nil, nil)
	if w.Code != 200 {
		t.Fatalf("old result status=%d", w.Code)
	}

	// return study version clones the finalized one
	w = e.do("POST",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/return", sid, vid), nil, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("return status=%d body=%s", w.Code, w.Body.String())
	}
	nv := e.mustJSON(w)["version"].(map[string]any)
	vid2 := nv["id"].(string)
	if nv["version_no"].(float64) != 2 {
		t.Fatalf("version_no=%v", nv["version_no"])
	}
	w = e.do("GET",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/measurements", sid, vid2), nil, nil)
	if len(e.mustJSON(w)["measurements"].([]any)) != 90 {
		t.Fatal("clone should have 90 readings")
	}
}

func TestConcurrentEditConflictHTTP(t *testing.T) {
	e := newEnv(t)
	sid, vid := e.createStudy(1.0)
	e.uploadPresetReal(sid, vid)

	// get one measurement
	w := e.do("GET",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/measurements", sid, vid), nil, nil)
	ms := e.mustJSON(w)["measurements"].([]any)
	m0 := ms[0].(map[string]any)
	mid := m0["id"].(string)
	rev := int64(m0["revision"].(float64))

	// first writer succeeds with If-Match-style revision in body
	w = e.do("PUT", "/api/v1/measurements/"+mid,
		map[string]any{"value": 42.5, "revision": rev}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("first PUT status=%d body=%s", w.Code, w.Body.String())
	}
	// second writer with the same stale revision -> 409 conflict
	w = e.do("PUT", "/api/v1/measurements/"+mid,
		map[string]any{"value": 99.9, "revision": rev}, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", w.Code, w.Body.String())
	}
	if code := e.mustJSON(w)["error"].(map[string]any)["code"]; code != "revision_conflict" {
		t.Fatalf("code=%v", code)
	}
	// value is the first writer's
	w = e.do("GET", "/api/v1/measurements/"+mid, nil, nil)
	if v := e.mustJSON(w)["measurement"].(map[string]any)["value"].(float64); v != 42.5 {
		t.Fatalf("value=%v, want 42.5", v)
	}
}

func TestNonFiniteValuePointsAtFieldHTTP(t *testing.T) {
	e := newEnv(t)
	sid, vid := e.createStudy(1.0)

	w := e.do("POST",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/measurements", sid, vid),
		`{"part":"P1","operator":"A","trial":1,"value":NaN}`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("NaN status=%d body=%s", w.Code, w.Body.String())
	}
	fes := e.mustJSON(w)["error"].(map[string]any)["field_errors"].([]any)
	if fes[0].(map[string]any)["field"] != "value" {
		t.Fatalf("field=%v", fes[0])
	}

	// batch: only the offending index is named
	body := `{"readings":[
		{"part":"P1","operator":"A","trial":1,"value":1.0},
		{"part":"P1","operator":"A","trial":2,"value":1e999},
		{"part":"P1","operator":"B","trial":1,"value":-Infinity}
	]}`
	w = e.do("POST",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/measurements/batch", sid, vid),
		body, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("batch bad number status=%d", w.Code)
	}
	fes = e.mustJSON(w)["error"].(map[string]any)["field_errors"].([]any)
	gotFields := map[string]bool{}
	for _, x := range fes {
		gotFields[x.(map[string]any)["field"].(string)] = true
	}
	if !gotFields["readings[1].value"] || !gotFields["readings[2].value"] {
		t.Fatalf("fields=%v", gotFields)
	}
	if gotFields["readings[0].value"] {
		t.Fatal("valid reading flagged")
	}
}

func TestResultStalenessHTTP(t *testing.T) {
	e := newEnv(t)
	sid, vid := e.createStudy(1.0)
	e.uploadPresetReal(sid, vid)
	w := e.do("POST",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/results", sid, vid), nil, nil)
	rid := e.mustJSON(w)["result"].(map[string]any)["id"].(string)

	// change one value
	w = e.do("GET",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/measurements", sid, vid), nil, nil)
	m0 := e.mustJSON(w)["measurements"].([]any)[0].(map[string]any)
	w = e.do("PUT", "/api/v1/measurements/"+m0["id"].(string),
		map[string]any{"value": m0["value"].(float64) + 1, "revision": int64(m0["revision"].(float64))}, nil)
	if w.Code != 200 {
		t.Fatalf("update status=%d", w.Code)
	}
	// old result now reports stale=true but is still served
	w = e.do("GET", "/api/v1/results/"+rid, nil, nil)
	if w.Code != 200 {
		t.Fatalf("get result status=%d", w.Code)
	}
	if e.mustJSON(w)["stale"] != true {
		t.Fatal("old result should be stale after data change")
	}
	// recompute: the new result is stored alongside the stale old one
	w = e.do("POST",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/results", sid, vid), nil, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("recompute status=%d body=%s", w.Code, w.Body.String())
	}
	w = e.do("GET",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/results", sid, vid), nil, nil)
	if n := len(e.mustJSON(w)["results"].([]any)); n != 2 {
		t.Fatalf("results count=%d, want 2 (old stale + new)", n)
	}
}

func TestInsufficientDesignHTTP(t *testing.T) {
	e := newEnv(t)
	sid, vid := e.createStudy(1.0)
	// one part, two operators -> 422 insufficient_design on compute
	w := e.do("POST",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/measurements/batch", sid, vid),
		map[string]any{"readings": []map[string]any{
			{"part": "P1", "operator": "A", "trial": 1, "value": 1.0},
			{"part": "P1", "operator": "A", "trial": 2, "value": 2.0},
			{"part": "P1", "operator": "B", "trial": 1, "value": 3.0},
			{"part": "P1", "operator": "B", "trial": 2, "value": 4.0},
		}}, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("batch status=%d", w.Code)
	}
	w = e.do("POST",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/results", sid, vid), nil, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("compute status=%d body=%s, want 422", w.Code, w.Body.String())
	}
	if code := e.mustJSON(w)["error"].(map[string]any)["code"]; code != "insufficient_design" {
		t.Fatalf("code=%v", code)
	}
}

func TestNotFoundAndCrossStudy(t *testing.T) {
	e := newEnv(t)
	if w := e.do("GET", "/api/v1/studies/nope", nil, nil); w.Code != 404 {
		t.Fatalf("missing study status=%d", w.Code)
	}
	sid, vid := e.createStudy(1.0)
	// version id from another (nonexistent) study -> 404
	if w := e.do("GET",
		fmt.Sprintf("/api/v1/studies/%s/versions/%s/measurements", sid, vid)+"x", nil, nil); w.Code != 404 {
		t.Fatalf("cross/unknown version status=%d", w.Code)
	}
}
