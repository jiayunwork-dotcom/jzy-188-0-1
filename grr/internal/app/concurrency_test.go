package app_test

import (
	"net/http"
	"sync"
	"testing"
)

// Two tablets PUT the same reading concurrently, both believing they hold
// row_version=1. Exactly one must succeed; the other gets 409.
func TestConcurrentEditConflict(t *testing.T) {
	s := newServer(t, t.TempDir())
	_, draftID := mustCreateStudy(s)
	code, body := s.do("POST", "/api/v1/versions/"+draftID+"/measurements", map[string]any{
		"part_id": "A", "operator_id": "Ana", "trial": 1, "value": 10,
	}, nil)
	if code != http.StatusCreated {
		t.Fatalf("add: %d %v", code, body)
	}
	_, body = s.do("GET", "/api/v1/versions/"+draftID+"/measurements", nil, nil)
	mid := body["measurements"].([]any)[0].(map[string]any)["id"].(string)

	var wg sync.WaitGroup
	results := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _ := s.do("PUT", "/api/v1/versions/"+draftID+"/measurements/"+mid, map[string]any{
				"part_id": "A", "operator_id": "Ana", "trial": 1,
				"value": float64(11 + i), "expected_row_version": 1,
			}, nil)
			results[i] = code
		}(i)
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, c := range results {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			t.Fatalf("unexpected status %d", c)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("want exactly one 200 and one 409, got %v", results)
	}

	// Winner can continue with the new version; loser retrying with stale
	// version still fails, but succeeds after reloading.
	_, body = s.do("GET", "/api/v1/versions/"+draftID+"/measurements", nil, nil)
	curRV := int(body["measurements"].([]any)[0].(map[string]any)["row_version"].(float64))
	if curRV != 2 {
		t.Fatalf("rv = %d", curRV)
	}
	code, _ = s.do("PUT", "/api/v1/versions/"+draftID+"/measurements/"+mid, map[string]any{
		"part_id": "A", "operator_id": "Ana", "trial": 1,
		"value": 99, "expected_row_version": 2,
	}, nil)
	if code != http.StatusOK {
		t.Fatalf("reload-then-update = %d", code)
	}
}
