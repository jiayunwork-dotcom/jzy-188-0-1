package msa

import "math"

// F-distribution upper-tail probabilities are computed directly from the
// regularized incomplete beta function:
//
//	P(F(d1,d2) > f) = I_{d2/(d2+d1*f)}(d2/2, d1/2)
//
// The incomplete beta uses Numerical-Recipes-style continued fractions
// (Lentz's method). No statistical library is pulled in.

const (
	betacfMaxIter = 200
	betacfEPS     = 3e-14
)

// betacf evaluates the continued fraction for the incomplete beta by the
// modified Lentz method (betacf from Numerical Recipes).
func betacf(a, b, x float64) float64 {
	qab := a + b
	qap := a + 1
	qam := a - 1
	c := 1.0
	d := 1.0 - qab*x/qap
	if math.Abs(d) < 1e-30 {
		d = 1e-30
	}
	d = 1.0 / d
	h := d
	for m := 1; m <= betacfMaxIter; m++ {
		m2 := 2 * m
		// Even step.
		aa := float64(m) * (b - float64(m)) * x / ((qam + float64(m2)) * (a + float64(m2)))
		d = 1.0 + aa*d
		if math.Abs(d) < 1e-30 {
			d = 1e-30
		}
		c = 1.0 + aa/c
		if math.Abs(c) < 1e-30 {
			c = 1e-30
		}
		d = 1.0 / d
		h *= d * c
		// Odd step.
		aa = -(a + float64(m)) * (qab + float64(m)) * x /
			((a + float64(m2)) * (qap + float64(m2)))
		d = 1.0 + aa*d
		if math.Abs(d) < 1e-30 {
			d = 1e-30
		}
		c = 1.0 + aa/c
		if math.Abs(c) < 1e-30 {
			c = 1e-30
		}
		d = 1.0 / d
		del := d * c
		h *= del
		if math.Abs(del-1.0) < betacfEPS {
			break
		}
	}
	return h
}

func lg(x float64) float64 {
	v, _ := math.Lgamma(x)
	return v
}

// betai computes the regularized incomplete beta I_x(a,b).
func betai(a, b, x float64) float64 {
	switch {
	case x <= 0:
		return 0
	case x >= 1:
		return 1
	}
	bt := math.Exp(lg(a+b) - lg(a) - lg(b) +
		a*math.Log(x) + b*math.Log(1.0-x))
	if x < (a+1.0)/(a+b+2.0) {
		return bt * betacf(a, b, x) / a
	}
	return 1.0 - bt*betacf(b, a, 1.0-x)/b
}

// FSurv returns the upper-tail p-value P(F(d1,d2) > f).
func FSurv(f, d1, d2 float64) float64 {
	if !finite(f) || !finite(d1) || !finite(d2) || d1 <= 0 || d2 <= 0 || f < 0 {
		return math.NaN()
	}
	if f == 0 {
		return 1
	}
	x := d2 / (d2 + d1*f)
	return betai(d2/2, d1/2, x)
}
