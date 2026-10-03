package stat

import (
	"math"
	"testing"
)

// Table-driven checks of the self-implemented F p-value against
// values with many published significant digits (common F tables and
// textbook examples), plus exact F(1,1) formula and limits.
func TestFPValueTable(t *testing.T) {
	cases := []struct {
		f, df1, df2, p float64
		tol            float64
	}{
		// F(2,27) 95% critical value 3.35413 -> upper tail 0.05
		{3.35413, 2, 27, 0.05, 5e-5},
		// F(9,18) 95% critical value ~2.4563
		{2.4563, 9, 18, 0.05, 5e-4},
		// F(2,9) 95% critical value 4.2565
		{4.2565, 2, 9, 0.05, 5e-4},
		// F(9,18) at 19.385 -> tiny tail
		{19.385, 9, 18, 1.64e-7, 1e-8},
		// F(1,1): exact upper tail 1 - (2/pi) atan(sqrt(f))
		{1, 1, 1, 0.5, 1e-12},
		{0.5, 1, 1, 1 - 2/math.Pi*math.Atan(math.Sqrt(0.5)), 1e-12},
		{2, 1, 1, 1 - 2/math.Pi*math.Atan(math.Sqrt(2)), 1e-12},
		// F(2,2) survival has closed form: P(F>f) = 1/(1+f), so
		// f=0.01 -> 1/1.01 = 0.990099...
		{0.01, 2, 2, 1 / 1.01, 1e-9},
	}
	for _, tc := range cases {
		got := FPValue(tc.f, tc.df1, tc.df2)
		if math.Abs(got-tc.p) > tc.tol {
			t.Errorf("FPValue(%v,%v,%v)=%.10f want %.10f", tc.f, tc.df1, tc.df2, got, tc.p)
		}
	}
}

func TestFPValueLimits(t *testing.T) {
	if got := FPValue(0, 2, 10); got != 1 {
		t.Errorf("f=0 -> p=1, got %v", got)
	}
	if got := FPValue(-5, 2, 10); got != 1 {
		t.Errorf("f<0 -> p=1, got %v", got)
	}
	if got := FPValue(math.Inf(1), 2, 10); got != 0 {
		t.Errorf("f=+Inf -> p=0, got %v", got)
	}
	if !math.IsNaN(FPValue(3, 0, 10)) {
		t.Error("df1=0 -> NaN expected")
	}
	if !math.IsNaN(FPValue(3, 2, 0)) {
		t.Error("df2=0 -> NaN expected")
	}
}

// TestFPValueMonotonic: p-value is strictly decreasing in f.
func TestFPValueMonotonic(t *testing.T) {
	prev := 1.0
	for f := 0.05; f < 100; f *= 1.5 {
		p := FPValue(f, 4, 12)
		if p >= prev {
			t.Fatalf("not decreasing at f=%v p=%v prev=%v", f, p, prev)
		}
		prev = p
	}
}
