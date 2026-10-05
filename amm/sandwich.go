package amm

import (
	"math/big"
)

// Trade is a single swap request in a sequence.
type Trade struct {
	Actor        string
	XToY         bool
	AmountIn     *big.Int
	MinAmountOut *big.Int // nil = no slippage protection
}

// TradeLog records the outcome of one trade in a sequence. All integer
// amounts are decimal strings so the log is directly JSON-serializable.
type TradeLog struct {
	Index          int     `json:"index"`
	Actor          string  `json:"actor"`
	Direction      string  `json:"direction"` // "X->Y" or "Y->X"
	AmountIn       string  `json:"amountIn"`
	AmountOut      string  `json:"amountOut"`
	Executed       bool    `json:"executed"`
	RejectReason   string  `json:"rejectReason,omitempty"`
	PriceBefore    float64 `json:"priceBefore"`    // Y per X
	PriceAfter     float64 `json:"priceAfter"`     // Y per X
	PriceJumpPct   float64 `json:"priceJumpPct"`   // signed % change of the mid price
	PriceImpactPct float64 `json:"priceImpactPct"` // 1 - execPrice/midPrice, in %
	Anomaly        bool    `json:"anomaly"`        // |PriceJumpPct| exceeded the threshold
	KBefore        string  `json:"kBefore"`
	KAfter         string  `json:"kAfter"`
}

func ratToFloat(r *big.Rat) float64 {
	f, _ := r.Float64()
	return f
}

// ExecuteSequence runs the trades against the pool in order, mutating it,
// and returns one log entry per trade. Rejected trades leave the pool
// unchanged. A trade whose absolute mid-price jump meets or exceeds
// anomalyThresholdPct (in percent) is flagged as an anomaly.
func ExecuteSequence(p *Pool, trades []Trade, anomalyThresholdPct float64) []TradeLog {
	logs := make([]TradeLog, 0, len(trades))
	for i, t := range trades {
		priceBefore := ratToFloat(p.MidPriceXInY())
		kBefore := p.K()
		dir := "X->Y"
		if !t.XToY {
			dir = "Y->X"
		}
		entry := TradeLog{
			Index:       i,
			Actor:       t.Actor,
			Direction:   dir,
			AmountIn:    t.AmountIn.String(),
			PriceBefore: priceBefore,
			KBefore:     kBefore.String(),
		}
		res, err := p.Swap(t.XToY, t.AmountIn, t.MinAmountOut)
		if err != nil {
			entry.Executed = false
			entry.RejectReason = err.Error()
			entry.AmountOut = "0"
			entry.PriceAfter = priceBefore
			entry.KAfter = kBefore.String()
		} else {
			entry.Executed = true
			entry.AmountOut = res.AmountOut.String()
			entry.PriceAfter = ratToFloat(p.MidPriceXInY())
			entry.PriceJumpPct = (entry.PriceAfter - entry.PriceBefore) / entry.PriceBefore * 100
			entry.PriceImpactPct = ratToFloat(res.PriceImpact) * 100
			entry.Anomaly = entry.PriceJumpPct >= anomalyThresholdPct ||
				entry.PriceJumpPct <= -anomalyThresholdPct
			entry.KAfter = p.K().String()
		}
		logs = append(logs, entry)
	}
	return logs
}

// Sandwich describes a detected sandwich attack: an attacker's front-run
// trade (same direction as the victim), the victim's trade, and the
// attacker's back-run trade (opposite direction). Indices refer to positions
// in the original trade slice passed to DetectSandwiches.
type Sandwich struct {
	FrontIndex     int    `json:"frontIndex"`
	VictimIndex    int    `json:"victimIndex"`
	BackIndex      int    `json:"backIndex"`
	Attacker       string `json:"attacker"`
	Victim         string `json:"victim"`
	VictimLoss     string `json:"victimLoss"`     // output the victim lost vs. no-attack baseline, in output-token units
	AttackerProfit string `json:"attackerProfit"` // attacker net gain, in the front-run input token
}

// executedTrade pairs a trade with its result during simulation.
type executedTrade struct {
	index  int // index in the original trade slice
	trade  Trade
	result *SwapResult
}

// simulate runs the trades against a clone of p and returns the executed
// trades (rejected ones are skipped, as on-chain they never happen).
func simulate(p *Pool, trades []Trade, skip map[int]bool) []executedTrade {
	pool := p.Clone()
	var out []executedTrade
	for i, t := range trades {
		if skip[i] {
			continue
		}
		res, err := pool.Swap(t.XToY, t.AmountIn, t.MinAmountOut)
		if err != nil {
			continue
		}
		out = append(out, executedTrade{index: i, trade: t, result: res})
	}
	return out
}

// DetectSandwiches scans the trade sequence for sandwich-attack patterns:
//
//	front: actor A trades in direction d
//	victim: a different actor trades in the same direction d
//	back: actor A trades in direction !d within `window` trades of the front
//
// For each (front, victim, back) triple it computes:
//   - VictimLoss: how much less output the victim received compared to a
//     baseline simulation in which the attacker's two trades are removed.
//   - AttackerProfit: back-run output minus front-run input, denominated in
//     the front-run's input token.
//
// Round trips that do not turn a profit are not reported: ordinary
// two-sided flow (a user buying and later selling) is not an attack.
func DetectSandwiches(p *Pool, trades []Trade, window int) []Sandwich {
	exec := simulate(p, trades, nil)
	byIndex := make(map[int]*SwapResult, len(exec))
	for _, e := range exec {
		byIndex[e.index] = e.result
	}

	var found []Sandwich
	for fi, f := range exec {
		for bi := fi + 1; bi < len(exec); bi++ {
			b := exec[bi]
			if b.trade.Actor != f.trade.Actor {
				continue
			}
			if b.trade.XToY == f.trade.XToY {
				continue // back-run must be the opposite direction
			}
			if b.index-f.index > window {
				break
			}
			// Attacker must not trade between front and back.
			interleaved := false
			for mi := fi + 1; mi < bi; mi++ {
				if exec[mi].trade.Actor == f.trade.Actor {
					interleaved = true
					break
				}
			}
			if interleaved {
				continue
			}
			// Every executed trade between front and back by another actor in
			// the front-run direction is a victim.
			for vi := fi + 1; vi < bi; vi++ {
				v := exec[vi]
				if v.trade.Actor == f.trade.Actor || v.trade.XToY != f.trade.XToY {
					continue
				}
				// Baseline: same sequence without the attacker's two trades.
				base := simulate(p, trades, map[int]bool{f.index: true, b.index: true})
				var baseVictimOut *big.Int
				for _, be := range base {
					if be.index == v.index {
						baseVictimOut = be.result.AmountOut
					}
				}
				loss := new(big.Int)
				if baseVictimOut != nil {
					loss.Sub(baseVictimOut, v.result.AmountOut)
				}
				profit := new(big.Int).Sub(b.result.AmountOut, f.trade.AmountIn)
				// A round trip that does not turn a profit is not an attack:
				// ordinary two-sided flow is filtered out here.
				if profit.Sign() <= 0 {
					continue
				}
				found = append(found, Sandwich{
					FrontIndex:     f.index,
					VictimIndex:    v.index,
					BackIndex:      b.index,
					Attacker:       f.trade.Actor,
					Victim:         v.trade.Actor,
					VictimLoss:     loss.String(),
					AttackerProfit: profit.String(),
				})
			}
		}
	}
	return found
}
