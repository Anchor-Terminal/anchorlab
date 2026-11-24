package eval

import "math"

// How sure a comparison is. gBench publishes no interval on accuracy, and on 59 questions two
// models a few points apart are often within luck of each other, so the lab says so where it
// compares them. Nothing here changes a score.

// SignTest is the two-sided p-value of an exact sign test on the questions two models disagree
// on: aOnly right for the first and not the second, bOnly the other way round. Questions both got
// right or both got wrong say nothing about which is better and aren't counted. Under 0.05 the
// difference is unlikely to be luck at this many questions; 1 when they never disagree.
func SignTest(aOnly, bOnly int) float64 {
	n, k := aOnly+bOnly, min(aOnly, bOnly)
	if n == 0 {
		return 1
	}
	// P(X <= k) for X ~ Binomial(n, 1/2), doubled, in logs so a few hundred questions don't overflow
	p := 0.0
	for i := 0; i <= k; i++ {
		p += math.Exp(lchoose(n, i) - float64(n)*math.Ln2)
	}
	return math.Min(1, 2*p)
}

func lchoose(n, k int) float64 {
	a, _ := math.Lgamma(float64(n + 1))
	b, _ := math.Lgamma(float64(k + 1))
	c, _ := math.Lgamma(float64(n - k + 1))
	return a - b - c
}

// Wilson is the 95% Wilson interval of k right out of n, in percent: where the share right could
// be with the luck of n questions. It stays inside 0 to 100, and is sensible at 0 and at n, where
// the plain interval isn't. n of 0 is 0 to 100.
func Wilson(k, n int) (lo, hi float64) {
	if n <= 0 {
		return 0, 100
	}
	const z = 1.959963984540054
	p, nf := float64(k)/float64(n), float64(n)
	den := 1 + z*z/nf
	mid := (p + z*z/(2*nf)) / den
	half := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / den
	return 100 * math.Max(0, mid-half), 100 * math.Min(1, mid+half)
}
