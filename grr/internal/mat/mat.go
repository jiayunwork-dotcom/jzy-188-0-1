// Package mat provides the small dense linear-algebra primitives used by the
// unbalanced-design ANOVA engine. Every matrix here is n x m float64 data;
// the GRR problems it serves have at most a few hundred rows.
package mat

import (
	"fmt"
	"math"
)

var (
	sqrt = math.Sqrt
	abs  = math.Abs
)

// Dense is a row-major dense matrix.
type Dense struct {
	v       [][]float64
	r, c, l int
}

// New returns an r x c zero matrix.
func New(r, c int) *Dense {
	v := make([][]float64, r)
	for i := range v {
		v[i] = make([]float64, c)
	}
	return &Dense{v: v, r: r, c: c, l: c}
}

// From builds a matrix from a slice of row slices (copied).
func From(rows [][]float64) *Dense {
	r := len(rows)
	c := 0
	if r > 0 {
		c = len(rows[0])
	}
	m := New(r, c)
	for i := range rows {
		copy(m.v[i], rows[i])
	}
	return m
}

// Rows / Cols report the shape.
func (m *Dense) Rows() int { return m.r }
func (m *Dense) Cols() int { return m.c }

// At reads an element.
func (m *Dense) At(i, j int) float64 { return m.v[i][j] }

// Set writes an element.
func (m *Dense) Set(i, j int, x float64) { m.v[i][j] = x }

// RowView returns the underlying row slice (no copy).
func (m *Dense) RowView(i int) []float64 { return m.v[i] }

// Clone returns a copy.
func (m *Dense) Clone() *Dense { return From(m.v) }

// T returns the transpose.
func (m *Dense) T() *Dense {
	o := New(m.c, m.r)
	for i := 0; i < m.r; i++ {
		for j := 0; j < m.c; j++ {
			o.v[j][i] = m.v[i][j]
		}
	}
	return o
}

// Mul computes a*b.
func Mul(a, b *Dense) *Dense {
	if a.c != b.r {
		panic(fmt.Sprintf("mat.Mul: shape %dx%d * %dx%d", a.r, a.c, b.r, b.c))
	}
	o := New(a.r, b.c)
	for i := 0; i < a.r; i++ {
		ar := a.v[i]
		for k := 0; k < a.c; k++ {
			br := b.v[k]
			x := ar[k]
			for j := 0; j < b.c; j++ {
				o.v[i][j] += x * br[j]
			}
		}
	}
	return o
}

// ATA computes X'X.
func ATA(x *Dense) *Dense {
	return Mul(x.T(), x)
}

// ATV computes X'y.
func ATV(x *Dense, y []float64) []float64 {
	if x.r != len(y) {
		panic("mat.ATV: length mismatch")
	}
	o := make([]float64, x.c)
	for i := 0; i < x.r; i++ {
		yi := y[i]
		for j := 0; j < x.c; j++ {
			o[j] += x.v[i][j] * yi
		}
	}
	return o
}

// Dot computes the Euclidean inner product.
func Dot(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// Norm2 computes the squared Euclidean norm.
func Norm2(a []float64) float64 { return Dot(a, a) }

// Frobenius computes trace(A'B), the Frobenius inner product.
func Frobenius(a, b *Dense) float64 {
	if a.r != b.r || a.c != b.c {
		panic("mat.Frobenius: shape mismatch")
	}
	s := 0.0
	for i := 0; i < a.r; i++ {
		for j := 0; j < a.c; j++ {
			s += a.v[i][j] * b.v[i][j]
		}
	}
	return s
}

// Hat wraps an orthonormal basis Q = X R^{-1} for a model space so that
// projection P v = Q (Q'v) is cheap.
type Hat struct {
	Q        *Dense // n x r orthonormal columns
	selected []int  // original column indices
}

// NewHat builds a projector onto col(X) via pivoted modified Gram-Schmidt.
func NewHat(x *Dense) *Hat { return NewHatWithTol(x, 1e-12) }

// NewHatWithTol is the rank-revealing QR construction: at each step the
// remaining column with the largest residual norm enters; the process stops
// once the largest residual squared norm falls below tol times the largest
// original column squared norm.
func NewHatWithTol(x *Dense, tol float64) *Hat {
	n, c := x.r, x.c
	remaining := make([]int, c)
	res := make([][]float64, c)
	biggest := 0.0
	for j := 0; j < c; j++ {
		remaining[j] = j
		res[j] = x.vCol(j)
		if v := Norm2(res[j]); v > biggest {
			biggest = v
		}
	}
	q := New(n, 0)
	sel := []int{}
	limit := tol * math.Max(biggest, 1)
	for len(remaining) > 0 {
		best, bestNorm2 := -1, 0.0
		for _, j := range remaining {
			if v := Norm2(res[j]); v > bestNorm2 {
				bestNorm2, best = v, j
			}
		}
		if bestNorm2 <= limit {
			break
		}
		qk := make([]float64, n)
		nrm := math.Sqrt(bestNorm2)
		for i := 0; i < n; i++ {
			qk[i] = res[best][i] / nrm
		}
		// Add column.
		nq := New(n, q.c+1)
		for i := 0; i < n; i++ {
			for j := 0; j < q.c; j++ {
				nq.v[i][j] = q.v[i][j]
			}
			nq.v[i][q.c] = qk[i]
		}
		q = nq
		sel = append(sel, best)
		// Deflate remaining columns.
		next := remaining[:0]
		for _, j := range remaining {
			if j == best {
				continue
			}
			d := Dot(res[j], qk)
			for i := 0; i < n; i++ {
				res[j][i] -= d * qk[i]
			}
			next = append(next, j)
		}
		remaining = next
	}
	return &Hat{Q: q, selected: sel}
}

// vCol returns column j as a slice.
func (m *Dense) vCol(j int) []float64 {
	o := make([]float64, m.r)
	for i := 0; i < m.r; i++ {
		o[i] = m.v[i][j]
	}
	return o
}

// ColViewVec returns the single column of a one-column matrix as a vector.
func (m *Dense) ColViewVec() []float64 {
	if m.c != 1 {
		panic("ColViewVec: matrix is not n x 1")
	}
	return m.vCol(0)
}

// Apply computes P v for a vector.
func (h *Hat) Apply(v []float64) []float64 {
	if h.Q.c == 0 {
		return make([]float64, h.Q.r)
	}
	coef := ATV(h.Q, v)
	o := make([]float64, h.Q.r)
	for i := 0; i < h.Q.r; i++ {
		for j := 0; j < h.Q.c; j++ {
			o[i] += h.Q.v[i][j] * coef[j]
		}
	}
	return o
}

// ApplyCols projects every column of Z: P Z.
func (h *Hat) ApplyCols(z *Dense) *Dense {
	if h.Q.c == 0 {
		return New(z.r, z.c)
	}
	coef := Mul(h.Q.T(), z)
	return Mul(h.Q, coef)
}

// Quad computes z' P z.
func (h *Hat) Quad(z []float64) float64 {
	pz := h.Apply(z)
	return Dot(z, pz)
}

// ---- small construction helpers used by the ANOVA engine ----

func ones(n int) []float64 {
	o := make([]float64, n)
	for i := range o {
		o[i] = 1
	}
	return o
}

// StackColumns builds [col0 | col1 | ...] where each col is length n.
func StackColumns(cols ...[]float64) *Dense {
	n := 0
	if len(cols) > 0 {
		n = len(cols[0])
	}
	m := New(n, len(cols))
	for j, c := range cols {
		for i := 0; i < n; i++ {
			m.v[i][j] = c[i]
		}
	}
	return m
}

func pickCols(x *Dense, idx []int) *Dense {
	o := New(x.r, len(idx))
	for j, k := range idx {
		for i := 0; i < x.r; i++ {
			o.v[i][j] = x.v[i][k]
		}
	}
	return o
}

// ColDiff returns a new matrix whose columns are z's columns minus P z.
func ColDiff(p *Hat, z *Dense) *Dense {
	pz := p.ApplyCols(z)
	o := New(z.r, z.c)
	for i := 0; i < z.r; i++ {
		for j := 0; j < z.c; j++ {
			o.v[i][j] = z.v[i][j] - pz.v[i][j]
		}
	}
	return o
}

// SolveSquare solves A x = b for a general square matrix A by Gaussian
// elimination with partial pivoting. ok=false indicates (numerical)
// singularity.
func SolveSquare(a *Dense, b []float64) (x []float64, ok bool) {
	n := a.r
	if a.c != n || len(b) != n {
		return nil, false
	}
	m := a.Clone()
	y := make([]float64, n)
	copy(y, b)
	for k := 0; k < n; k++ {
		piv, pv := k, abs(m.v[k][k])
		for i := k + 1; i < n; i++ {
			if abs(m.v[i][k]) > pv {
				pv, piv = abs(m.v[i][k]), i
			}
		}
		if pv < 1e-12 {
			return nil, false
		}
		if piv != k {
			m.v[k], m.v[piv] = m.v[piv], m.v[k]
			y[k], y[piv] = y[piv], y[k]
		}
		for i := k + 1; i < n; i++ {
			f := m.v[i][k] / m.v[k][k]
			for j := k; j < n; j++ {
				m.v[i][j] -= f * m.v[k][j]
			}
			y[i] -= f * y[k]
		}
	}
	x = make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		s := y[i]
		for j := i + 1; j < n; j++ {
			s -= m.v[i][j] * x[j]
		}
		x[i] = s / m.v[i][i]
	}
	return x, true
}

// LeastSquares solves x beta = y via normal equations (tiny systems only).
// ok=false if the normal matrix is singular.
func LeastSquares(x *Dense, y []float64) (beta []float64, ok bool) {
	return SolveSquare(ATA(x), ATV(x, y))
}
