// Package stat implements the statistical calculations used by the
// Gauge R&R service. No third-party statistics library is used: the
// F-distribution p-values are computed from the regularized incomplete
// beta function implemented in this package.
package stat

import (
	"errors"
	"math"
)

// FPValue returns P(F(df1,df2) > f), the upper-tail p-value of an
// F-distributed test statistic. df1 and df2 are the numerator and
// denominator degrees of freedom.
//
// Special cases:
//   - f <= 0           -> p = 1 (no evidence against H0)
//   - f is not finite  -> p = 0
//   - df2 == 0 (no denominator mean square available) -> NaN
//   - df1 == 0         -> NaN
func FPValue(f, df1, df2 float64) float64 {
	if math.IsNaN(df1) || math.IsNaN(df2) {
		return math.NaN()
	}
	if df1 <= 0 || df2 <= 0 {
		return math.NaN()
	}
	if math.IsInf(f, 1) {
		return 0
	}
	if math.IsNaN(f) || f <= 0 {
		return 1
	}
	// For F = (X/df1)/(Y/df2) with X~Chi2(df1), Y~Chi2(df2):
	//   P(F > f) = I_{df2/(df2+df1*f)}(df2/2, df1/2)
	x := df2 / (df2 + df1*f)
	a := df2 / 2
	b := df1 / 2
	return regularizedIncompleteBeta(x, a, b)
}

// regularizedIncompleteBeta computes I_x(a,b) = B(x;a,b)/B(a,b)
// for 0 <= x <= 1 and a,b > 0.
func regularizedIncompleteBeta(x, a, b float64) float64 {
	switch {
	case x <= 0:
		return 0
	case x >= 1:
		return 1
	}
	// Continued fraction is fastest when x < (a+1)/(a+b+2); otherwise
	// use the symmetry relation I_x(a,b) = 1 - I_{1-x}(b,a).
	if x < (a+1)/(a+b+2) {
		return betaCF(x, a, b)
	}
	return 1 - betaCF(1-x, b, a)
}

// betaCF evaluates I_x(a,b) via the incomplete beta continued fraction
// (Lentz's method), multiplied by the front factor
//
//	x^a (1-x)^b / (a B(a,b)).
func betaCF(x, a, b float64) float64 {
	const maxIter = 300
	const eps = 1e-15
	const tiny = 1e-300

	logFront := a*math.Log(x) + b*math.Log1p(-x)
	logBeta, _ := math.Lgamma(a)
	logBeta += func() float64 { v, _ := math.Lgamma(b); return v }()
	logBeta -= func() float64 { v, _ := math.Lgamma(a + b); return v }()
	front := math.Exp(logFront-logBeta) / a

	// Lentz's algorithm for the continued fraction of the incomplete beta.
	c := 1.0
	d := 1 - (a+b)*x/(a+1)
	if math.Abs(d) < tiny {
		d = tiny
	}
	d = 1 / d
	h := d
	for m := 1; m <= maxIter; m++ {
		mf := float64(m)
		m2 := 2 * mf

		// Even step coefficient (d_{2m}).
		num := mf * (b - mf) * x / ((a + m2 - 1) * (a + m2))
		d = 1 + num*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + num/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		h *= d * c

		// Odd step coefficient (d_{2m+1}).
		num = -((a + mf) * (a + b + mf) * x) / ((a + m2) * (a + m2 + 1))
		d = 1 + num*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + num/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		delta := d * c
		h *= delta
		if math.Abs(delta-1) < eps {
			break
		}
	}
	return front * h
}

// ErrInsufficientLevels is returned when fewer than two parts or
// operators are present.
var ErrInsufficientLevels = errors.New("at least 2 parts and 2 operators are required")
