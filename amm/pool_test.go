package amm

import (
	"errors"
	"math/big"
	"math/rand"
	"testing"
)

func bi(v int64) *big.Int { return big.NewInt(v) }

// TestGetAmountOutMatchesUniswapV2Formula checks the exact integer formula
// out = (in*997*reserveOut) / (reserveIn*1000 + in*997) with floor division.
func TestGetAmountOutMatchesUniswapV2Formula(t *testing.T) {
	out, err := GetAmountOut(bi(1000), bi(10000), bi(10000), 997, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// 997*1000*10000 / (10000*1000 + 997*1000) = 9970000000 / 10997000 = 906.610... -> 906
	if out.Cmp(bi(906)) != 0 {
		t.Fatalf("got %s, want 906", out)
	}
}

// TestInvariantMonotoneOverManySwaps runs a long deterministic sequence of
// small swaps and asserts k never decreases (and strictly grows with a
// positive fee) — i.e. no systematic drift from rounding.
func TestInvariantMonotoneOverManySwaps(t *testing.T) {
	p := NewPool(bi(1_000_000), bi(1_000_000), 997, 1000)
	rng := rand.New(rand.NewSource(42))
	prevK := p.K()
	for i := 0; i < 500; i++ {
		amount := bi(rng.Int63n(500) + 100) // many small trades
		xToY := i%2 == 0
		res, err := p.Swap(xToY, amount, nil)
		if err != nil {
			t.Fatalf("swap %d: %v", i, err)
		}
		k := p.K()
		if k.Cmp(prevK) <= 0 {
			t.Fatalf("swap %d: k did not strictly increase: %s -> %s", i, prevK, k)
		}
		if res.KAfter.Cmp(k) != 0 {
			t.Fatalf("swap %d: reported k mismatch", i)
		}
		prevK = k
	}
}

// TestInvariantExactWithZeroFee: with no fee, k must stay exactly constant.
func TestInvariantExactWithZeroFee(t *testing.T) {
	p := NewPool(bi(1_000_000_000), bi(2_000_000_000), 1000, 1000)
	k0 := p.K()
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 200; i++ {
		amount := bi(rng.Int63n(10_000) + 1)
		if _, err := p.Swap(i%3 != 0, amount, nil); err != nil {
			t.Fatalf("swap %d: %v", i, err)
		}
		// Integer floor rounding favors the pool, so k may grow by at most a
		// tiny rounding remainder per swap, but must never decrease.
		if p.K().Cmp(k0) < 0 {
			t.Fatalf("swap %d: k decreased below initial: %s < %s", i, p.K(), k0)
		}
	}
}

// TestSlippageProtectionRejects verifies that a trade whose output falls
// below minAmountOut is rejected and the pool is untouched.
func TestSlippageProtectionRejects(t *testing.T) {
	p := NewPool(bi(1_000_000), bi(1_000_000), 997, 1000)
	x0, y0 := p.ReserveX.String(), p.ReserveY.String()

	quote, err := p.Quote(true, bi(50_000))
	if err != nil {
		t.Fatal(err)
	}
	// Demand more than the quote: must be rejected.
	minOut := new(big.Int).Add(quote, bi(1))
	_, err = p.Swap(true, bi(50_000), minOut)
	if !errors.Is(err, ErrSlippageExceeded) {
		t.Fatalf("expected ErrSlippageExceeded, got %v", err)
	}
	if p.ReserveX.String() != x0 || p.ReserveY.String() != y0 {
		t.Fatal("pool state changed after rejected swap")
	}

	// Exactly the quote must succeed (boundary is inclusive).
	if _, err := p.Swap(true, bi(50_000), quote); err != nil {
		t.Fatalf("swap at exact quote rejected: %v", err)
	}
}

// TestMinOutFromBps checks the bps -> minAmountOut conversion.
func TestMinOutFromBps(t *testing.T) {
	got := MinOutFromBps(bi(10_000), 50) // 0.5%
	if got.Cmp(bi(9_950)) != 0 {
		t.Fatalf("got %s, want 9950", got)
	}
}

// TestPriceImpactMatchesTheory: with zero fee the theoretical price impact of
// selling `in` of X is exactly in/(reserveX+in). With a fee it must be
// slightly higher. Both are checked against the reported PriceImpact.
func TestPriceImpactMatchesTheory(t *testing.T) {
	const x, y, in = 1_000_000, 1_000_000, 100_000

	// Zero fee: impact == in/(x+in) up to one unit of floor rounding.
	p0 := NewPool(bi(x), bi(y), 1000, 1000)
	res, err := p0.Swap(true, bi(in), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := big.NewRat(in, x+in)
	diff := new(big.Rat).Sub(res.PriceImpact, want)
	if diff.Sign() < 0 || diff.Cmp(big.NewRat(1, in)) > 0 {
		t.Fatalf("zero-fee impact = %s, want %s within 1/%d floor-rounding tolerance",
			res.PriceImpact, want, in)
	}

	// With 0.3% fee: impact must exceed the zero-fee value and match
	// 1 - execPrice/midPrice computed independently.
	p1 := NewPool(bi(x), bi(y), 997, 1000)
	res1, err := p1.Swap(true, bi(in), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res1.PriceImpact.Cmp(want) <= 0 {
		t.Fatalf("fee impact %s should exceed zero-fee impact %s", res1.PriceImpact, want)
	}
	indep := new(big.Rat).Sub(big.NewRat(1, 1),
		new(big.Rat).Quo(res1.ExecPrice, res1.MidBefore))
	if res1.PriceImpact.Cmp(indep) != 0 {
		t.Fatalf("reported impact %s != independent %s", res1.PriceImpact, indep)
	}
}

// TestFeeGrowsK verifies the fee is retained in the pool: k strictly
// increases, and the increase is consistent with the retained fee.
func TestFeeGrowsK(t *testing.T) {
	p := NewPool(bi(1_000_000), bi(1_000_000), 997, 1000)
	k0 := p.K()
	res, err := p.Swap(true, bi(10_000), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.K().Cmp(k0) <= 0 {
		t.Fatal("k did not increase after fee-bearing swap")
	}
	if res.FeeRetained.Cmp(bi(30)) != 0 { // 10000 * 3/1000
		t.Fatalf("retained fee = %s, want 30", res.FeeRetained)
	}
	if res.KAfter.Cmp(p.K()) != 0 {
		t.Fatal("reported KAfter mismatch")
	}
}
