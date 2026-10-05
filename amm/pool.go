// Package amm implements a constant-product automated market maker
// (x * y = k, Uniswap v2 core model) with exact integer arithmetic,
// fee-aware swaps, and slippage protection.
//
// All reserve and amount math uses math/big integers with floor division,
// exactly like Uniswap v2's getAmountOut. Rounding always favors the pool,
// so the invariant k = x * y never decreases due to rounding, and the
// retained fee makes k strictly increase on every non-zero swap. There is
// no floating-point drift across long sequences of small trades.
package amm

import (
	"errors"
	"fmt"
	"math/big"
)

var (
	// ErrZeroAmount is returned when a swap or quote has a non-positive input.
	ErrZeroAmount = errors.New("amm: amount in must be positive")
	// ErrInsufficientLiquidity is returned when reserves are non-positive or
	// the computed output rounds to zero.
	ErrInsufficientLiquidity = errors.New("amm: insufficient liquidity")
	// ErrSlippageExceeded is returned when the computed output is below the
	// caller's minAmountOut. The pool state is left unchanged.
	ErrSlippageExceeded = errors.New("amm: slippage tolerance exceeded")
)

// Pool is a constant-product AMM pool over two tokens X and Y.
//
// Fee handling: a fraction (1 - FeeNum/FeeDen) of every input amount is
// retained in the pool (e.g. FeeNum=997, FeeDen=1000 is a 0.3% fee). The
// fee is NOT skimmmed off; it stays in the reserves, so after a swap
//
//	k' = (x + in) * (y - out) >= k
//
// with strict inequality whenever the fee is positive and in > 0. This is
// the Uniswap v2 convention and is applied consistently to every swap.
type Pool struct {
	ReserveX *big.Int
	ReserveY *big.Int
	FeeNum   int64 // fee fraction numerator, e.g. 997
	FeeDen   int64 // fee fraction denominator, e.g. 1000
}

// NewPool creates a pool with the given reserves and fee fraction.
func NewPool(x, y *big.Int, feeNum, feeDen int64) *Pool {
	return &Pool{
		ReserveX: new(big.Int).Set(x),
		ReserveY: new(big.Int).Set(y),
		FeeNum:   feeNum,
		FeeDen:   feeDen,
	}
}

// Clone returns a deep copy of the pool.
func (p *Pool) Clone() *Pool {
	return NewPool(p.ReserveX, p.ReserveY, p.FeeNum, p.FeeDen)
}

// K returns the invariant x * y.
func (p *Pool) K() *big.Int {
	return new(big.Int).Mul(p.ReserveX, p.ReserveY)
}

// MidPriceXInY returns the spot (marginal) price of X denominated in Y.
func (p *Pool) MidPriceXInY() *big.Rat {
	return new(big.Rat).SetFrac(p.ReserveY, p.ReserveX)
}

// GetAmountOut computes the output amount for a given input, using the
// Uniswap v2 formula with floor division:
//
//	amountInWithFee = amountIn * FeeNum
//	out = amountInWithFee * reserveOut / (reserveIn * FeeDen + amountInWithFee)
func GetAmountOut(amountIn, reserveIn, reserveOut *big.Int, feeNum, feeDen int64) (*big.Int, error) {
	if amountIn.Sign() <= 0 {
		return nil, ErrZeroAmount
	}
	if reserveIn.Sign() <= 0 || reserveOut.Sign() <= 0 {
		return nil, ErrInsufficientLiquidity
	}
	amountInWithFee := new(big.Int).Mul(amountIn, big.NewInt(feeNum))
	numerator := new(big.Int).Mul(amountInWithFee, reserveOut)
	denominator := new(big.Int).Mul(reserveIn, big.NewInt(feeDen))
	denominator.Add(denominator, amountInWithFee)
	out := new(big.Int).Quo(numerator, denominator)
	if out.Sign() <= 0 {
		return nil, ErrInsufficientLiquidity
	}
	return out, nil
}

// Quote returns the output amount for a swap in the given direction without
// mutating the pool.
func (p *Pool) Quote(xToY bool, amountIn *big.Int) (*big.Int, error) {
	in, out := p.ReserveX, p.ReserveY
	if !xToY {
		in, out = p.ReserveY, p.ReserveX
	}
	return GetAmountOut(amountIn, in, out, p.FeeNum, p.FeeDen)
}

// MinOutFromBps converts a quote and a maximum slippage tolerance in basis
// points into a minAmountOut threshold: quote * (10000 - bps) / 10000.
func MinOutFromBps(quote *big.Int, maxSlippageBps int64) *big.Int {
	m := new(big.Int).Mul(quote, big.NewInt(10000-maxSlippageBps))
	return m.Quo(m, big.NewInt(10000))
}

// SwapResult describes an executed swap.
type SwapResult struct {
	XToY        bool
	AmountIn    *big.Int
	AmountOut   *big.Int
	FeeRetained *big.Int  // fee kept in the pool, in input-token units
	MidBefore   *big.Rat  // spot price of the input token (in output-token units) before the swap
	ExecPrice   *big.Rat  // effective execution price: amountOut / amountIn
	PriceImpact *big.Rat  // 1 - ExecPrice/MidBefore (0 = no impact, 1 = total)
	KBefore     *big.Int
	KAfter      *big.Int
}

// Swap executes a swap of the input token for the output token.
// xToY=true sells X for Y; xToY=false sells Y for X.
//
// Slippage protection: if minAmountOut is non-nil and the computed output is
// strictly less than it, the swap is rejected with ErrSlippageExceeded and
// the pool state is left completely unchanged.
func (p *Pool) Swap(xToY bool, amountIn, minAmountOut *big.Int) (*SwapResult, error) {
	resIn, resOut := p.ReserveX, p.ReserveY
	if !xToY {
		resIn, resOut = p.ReserveY, p.ReserveX
	}
	amountOut, err := GetAmountOut(amountIn, resIn, resOut, p.FeeNum, p.FeeDen)
	if err != nil {
		return nil, err
	}
	if minAmountOut != nil && amountOut.Cmp(minAmountOut) < 0 {
		return nil, fmt.Errorf("%w: got %s, require at least %s",
			ErrSlippageExceeded, amountOut.String(), minAmountOut.String())
	}

	kBefore := p.K()
	midBefore := new(big.Rat).SetFrac(resOut, resIn)

	resIn.Add(resIn, amountIn)
	resOut.Sub(resOut, amountOut)

	fee := new(big.Int).Mul(amountIn, big.NewInt(p.FeeDen-p.FeeNum))
	fee.Quo(fee, big.NewInt(p.FeeDen))

	exec := new(big.Rat).SetFrac(amountOut, amountIn)
	impact := new(big.Rat).Sub(big.NewRat(1, 1), new(big.Rat).Quo(exec, midBefore))

	return &SwapResult{
		XToY:        xToY,
		AmountIn:    new(big.Int).Set(amountIn),
		AmountOut:   amountOut,
		FeeRetained: fee,
		MidBefore:   midBefore,
		ExecPrice:   exec,
		PriceImpact: impact,
		KBefore:     kBefore,
		KAfter:      p.K(),
	}, nil
}
