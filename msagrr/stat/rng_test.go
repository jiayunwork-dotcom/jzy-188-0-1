package stat

import (
	"math"
	"math/rand/v2"
)

// rng is a tiny deterministic random source for tests built on the
// stdlib PCG generator with a fixed seed (reproducible across runs).
type rng struct {
	r *rand.Rand
}

func newRng(seed uint64) *rng {
	return &rng{r: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
}

func (g *rng) Intn(n int) int   { return g.r.IntN(n) }
func (g *rng) Perm(n int) []int { return g.r.Perm(n) }
func (g *rng) Float64() float64 { return g.r.Float64() }

// Normal returns a standard-normal sample via Box-Muller.
func (g *rng) Normal() float64 {
	u1 := 1 - g.r.Float64()
	u2 := g.r.Float64()
	return math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
}
