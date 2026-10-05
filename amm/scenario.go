package amm

import (
	"math/big"
)

// TokenMeta 描述一个代币的符号与小数位数。
type TokenMeta struct {
	Symbol   string
	Decimals uint
}

// ScenarioSpec 描述一个待模拟的交易序列场景。
type ScenarioSpec struct {
	ID, Name, Description string
	Token0, Token1        TokenMeta
	StartR0, StartR1      *big.Int
	FeeBps                uint64
	Requests              []TradeReq
}

// ScenarioResult 是一个场景执行 + 检测后的完整结果。
type ScenarioResult struct {
	Spec       ScenarioSpec
	Pool       *Pool
	Steps      []StepResult
	Jumps      []Jump
	Patterns   []Pattern
	Sandwiches []Sandwich
}

// RunScenario 依次执行请求序列。EchoOutputOf 指向的“前一笔输出”在该笔已
// 成交时解析为其精确输出数量（攻击者把建仓所得全额回投）；若被引用的交易
// 未成交，则当前请求按非法输入拒绝。
func RunScenario(spec ScenarioSpec) *ScenarioResult {
	pool := NewPool(spec.StartR0, spec.StartR1, spec.FeeBps)
	res := &ScenarioResult{Spec: spec, Pool: pool}

	outputs := make(map[int]*big.Int) // seq -> 已成交交易的输出
	for _, req := range spec.Requests {
		if req.EchoOutputOf > 0 {
			out, ok := outputs[req.EchoOutputOf]
			if !ok {
				req.AmountIn = nil // 引用的交易未成交 → 无法平仓
				if req.Note == "" {
					req.Note = "回投前序成交输出，但该交易未成交"
				}
			} else {
				req.AmountIn = new(big.Int).Set(out)
			}
		}
		r := pool.Execute(req)
		if r.Status == StatusExecuted {
			outputs[r.Seq] = new(big.Int).Set(r.AmountOut)
		}
		res.Steps = append(res.Steps, r)
	}

	Detect(res)
	return res
}

// PriceHuman 以 float64 返回某储备状态下的现货价 token1/token0，仅供展示。
func PriceHuman(r0, r1 *big.Int, d0, d1 uint) float64 {
	p := new(big.Rat).SetFrac(r1, r0)
	// 换算小数位：price_human = (r1/10^d1)/(r0/10^d0) = r1/r0 * 10^(d0-d1)
	if d0 != d1 {
		exp := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d0)-int64(d1)), nil)
		p.Mul(p, new(big.Rat).SetInt(exp))
	}
	f, _ := p.Float64()
	return f
}

// HumanAmount 把最小整数单位金额换算为人类可读 float64，仅供展示。
func HumanAmount(raw *big.Int, decimals uint) float64 {
	if raw == nil {
		return 0
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	f, _ := new(big.Rat).SetFrac(raw, scale).Float64()
	return f
}

// StepPrice 返回某一步成交后（被拒绝时为拒绝发生时）的现货价，仅供展示。
func (s *ScenarioResult) StepPrice(st StepResult) float64 {
	return PriceHuman(st.AfterR0, st.AfterR1, s.Spec.Token0.Decimals, s.Spec.Token1.Decimals)
}
