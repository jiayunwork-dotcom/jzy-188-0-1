package httpapi

import "testing"

func TestScanLooseJSON(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		paths []string
		badOK bool
	}{
		{"finite", `{"value":3.5,"tolerance":1}`, nil, false},
		{"nan scalar", `{"value":NaN}`, []string{"value"}, false},
		{"infinity nested", `{"readings":[{"value":Infinity},{"value":-Infinity}]}`,
			[]string{"readings[0].value", "readings[1].value"}, false},
		{"overflow exponent", `{"tolerance":1e999}`, []string{"tolerance"}, false},
		{"valid strings unicode", `{"part":"P1","note":"µm ☃"}`, nil, false},
		{"escaped string", `{"part":"P\"1","operator":"A\nB"}`, nil, false},
		{"nested array index", `{"a":{"b":[{"x":NaN},{"x":2},{"x":NaN}]}}`,
			[]string{"a.b[0].x", "a.b[2].x"}, false},
		{"malformed", `{"value":`, nil, true},
		{"trailing garbage", `{"value":1}xx`, nil, true},
		{"bool null", `{"ok":true,"z":null,"v":-0.25}`, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad, err := scanLooseJSON([]byte(tc.body))
			if tc.badOK {
				if err == nil {
					t.Fatal("expected parse error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(bad) != len(tc.paths) {
				t.Fatalf("bad count=%d (%+v), want %v", len(bad), bad, tc.paths)
			}
			for i, want := range tc.paths {
				if bad[i].Path != want {
					t.Errorf("path[%d]=%q want %q (raw=%s)", i, bad[i].Path, want, bad[i].Raw)
				}
			}
		})
	}
}
