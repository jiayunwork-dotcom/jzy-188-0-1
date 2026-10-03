package mat

import (
	"math"
	"testing"
)

func TestProjectorProperties(t *testing.T) {
	// Columns with an exact dependency: third column = col1 + col2.
	x := From([][]float64{
		{1, 0, 1},
		{1, 0, 1},
		{0, 1, 1},
		{0, 1, 1},
		{1, 1, 2},
	})
	h := NewHat(x)
	if h.Q.Cols() != 2 {
		t.Fatalf("rank = %d, want 2", h.Q.Cols())
	}
	y := []float64{3, 1, 4, 1, 5}
	py := h.Apply(y)
	// Py must lie in col(X).
	xx := x
	// Residual orthogonal to each basis vector.
	for j := 0; j < h.Q.Cols(); j++ {
		s := 0.0
		for i := range y {
			s += (y[i] - py[i]) * h.Q.At(i, j)
		}
		if math.Abs(s) > 1e-10 {
			t.Fatalf("residual not orthogonal to q%d: %.3e", j, s)
		}
	}
	// P is idempotent: P(Py) == Py.
	p2 := h.Apply(py)
	for i := range py {
		if math.Abs(p2[i]-py[i]) > 1e-12 {
			t.Fatalf("P not idempotent at %d: %.12f vs %.12f", i, p2[i], py[i])
		}
	}
	_ = xx
}

func TestSolveSquareAndLeastSquares(t *testing.T) {
	a := From([][]float64{
		{2, 1, 0},
		{1, 3, 1},
		{0, 1, 2},
	})
	b := []float64{1, 2, 3}
	x, ok := SolveSquare(a, b)
	if !ok {
		t.Fatal("solve failed")
	}
	for i := 0; i < 3; i++ {
		s := 0.0
		for j := 0; j < 3; j++ {
			s += a.At(i, j) * x[j]
		}
		if math.Abs(s-b[i]) > 1e-10 {
			t.Fatalf("row %d: %.6f != %.6f", i, s, b[i])
		}
	}
	// Singular matrix -> ok=false.
	s := From([][]float64{{1, 2}, {2, 4}})
	if _, ok := SolveSquare(s, []float64{1, 2}); ok {
		t.Fatal("singular matrix reported solvable")
	}
	// LS on a 3x2 overdetermined consistent system.
	a2 := From([][]float64{{1, 0}, {0, 1}, {1, 1}})
	y2 := []float64{2, 3, 5}
	x2, ok := LeastSquares(a2, y2)
	if !ok || math.Abs(x2[0]-2) > 1e-9 || math.Abs(x2[1]-3) > 1e-9 {
		t.Fatalf("ls = %v ok=%v", x2, ok)
	}
}
