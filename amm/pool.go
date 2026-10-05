// Package amm 实现恒定乘积做市商（constant-product AMM，Uniswap v2 核心模型）
// 的交易执行、滑点保护、价格冲击度量与三明治攻击检测。
//
// 所有储备量与金额一律使用 math/big.Int 精确整数表示，全程不使用浮点参与
// 定价，因此连续多笔小额交易不会产生任何浮点漂移；浮点仅在生成报告时用于
// 人类可读的展示换算。
package amm

import (
	"fmt"
	"math/big"
	"time"
)

// BpsWhole 是基点（basis points）的分母：10000 bps = 100%。
const BpsWhole = int64(10000)

// Side 表示交易方向。
type Side uint8

const (
	// SellToken0：支付 token0，买入 token1（盘口语言：BUY_TOKEN1）。
	SellToken0 Side = iota
	// SellToken1：支付 token1，买入 token0（盘口语言：BUY_TOKEN0）。
	SellToken1
)

// String 返回与报告 JSON 一致的盘口方向标识。
func (s Side) String() string {
	if s == SellToken0 {
		return "BUY_TOKEN1"
	}
	return "BUY_TOKEN0"
}

// 交易结果状态。
const (
	StatusExecuted         = "executed"
	StatusRejectedSlippage = "rejected_slippage"
	StatusRejectedInvalid  = "rejected_invalid"
)

// 拒绝原因码。
const (
	ReasonInvalidInput       = "INVALID_INPUT"
	ReasonSlippageProtection = "SLIPPAGE_PROTECTION"
)

// Pool 是一个恒定乘积资金池。字段不要在包外直接修改。
type Pool struct {
	r0     *big.Int // token0 储备（最小整数单位）
	r1     *big.Int // token1 储备（最小整数单位）
	feeBps uint64   // 输入端手续费，基点；30 即 0.30%
}

// NewPool 以初始储备与费率（bps）创建资金池。
func NewPool(r0, r1 *big.Int, feeBps uint64) *Pool {
	if feeBps >= uint64(BpsWhole) {
		panic(fmt.Sprintf("amm: feeBps must be in [0,10000), got %d", feeBps))
	}
	return &Pool{r0: new(big.Int).Set(r0), r1: new(big.Int).Set(r1), feeBps: feeBps}
}

// R0 返回 token0 储备的副本。
func (p *Pool) R0() *big.Int { return new(big.Int).Set(p.r0) }

// R1 返回 token1 储备的副本。
func (p *Pool) R1() *big.Int { return new(big.Int).Set(p.r1) }

// FeeBps 返回费率（bps）。
func (p *Pool) FeeBps() uint64 { return p.feeBps }

// K 返回当前不变量 k = r0 * r1。
func (p *Pool) K() *big.Int {
	return new(big.Int).Mul(p.r0, p.r1)
}

// Quote 是一笔交易在给定方向上的精确报价（不改变池状态）。
type Quote struct {
	AmountIn    *big.Int // 用户支付的含手续费总额
	AmountInNet *big.Int // 实际参与定价的净额（扣除手续费后）
	Fee         *big.Int // 留在池内、不参与换出的手续费
	AmountOut   *big.Int // 换出数量（整数向下取整）
	NewR0       *big.Int // 若成交：成交后 token0 储备
	NewR1       *big.Int // 若成交：成交后 token1 储备
}

// Quote 返回按当前储备对 amountIn 的报价。amountIn 必须为正数。
//
// 定价公式（Uniswap v2）：
//
//	amountInNet = floor(amountIn * (10000 - feeBps) / 10000)
//	amountOut   = floor(amountInNet * reserveOut / (reserveIn + amountInNet))
//
// 手续费口径（全文件统一）：手续费在输入端扣除，只有 amountInNet 参与定价；
// 但手续费并不离开资金池——输入侧储备增加的是含手续费的完整 amountIn：
//
//	reserveIn'  = reserveIn + amountIn
//	reserveOut' = reserveOut - amountOut
//
// 因此每笔成交后 k' = reserveIn' * reserveOut' >= k 严格成立（手续费留存给
// LP；零费率时取整也只可能让 k 不减），不存在浮点漂移意义下的“系统性偏离”。
func (p *Pool) Quote(side Side, amountIn *big.Int) (*Quote, error) {
	if amountIn == nil || amountIn.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be greater than 0")
	}
	rIn, rOut := p.r0, p.r1
	if side == SellToken1 {
		rIn, rOut = p.r1, p.r0
	}
	inNet, fee, out := quoteAt(rIn, rOut, amountIn, p.feeBps)
	if out.Sign() == 0 {
		return nil, fmt.Errorf("output amount is 0: input too small relative to pool reserves")
	}
	q := &Quote{
		AmountIn:    new(big.Int).Set(amountIn),
		AmountInNet: inNet,
		Fee:         fee,
		AmountOut:   out,
	}
	if side == SellToken0 {
		q.NewR0 = new(big.Int).Add(rIn, amountIn)
		q.NewR1 = new(big.Int).Sub(rOut, out)
	} else {
		q.NewR1 = new(big.Int).Add(rIn, amountIn)
		q.NewR0 = new(big.Int).Sub(rOut, out)
	}
	return q, nil
}

// quoteAt 是纯函数版本的恒定乘积报价，可对任意“假设储备”计价
// （用于滑点保护的无操纵公允报价与攻击损失核算）。
func quoteAt(rIn, rOut, amountIn *big.Int, feeBps uint64) (inNet, fee, out *big.Int) {
	keep := big.NewInt(BpsWhole - int64(feeBps))
	whole := big.NewInt(BpsWhole)
	inNet = new(big.Int).Div(new(big.Int).Mul(amountIn, keep), whole) // floor
	fee = new(big.Int).Sub(new(big.Int).Set(amountIn), inNet)
	out = new(big.Int).Div(
		new(big.Int).Mul(inNet, rOut),
		new(big.Int).Add(rIn, inNet),
	) // floor
	return inNet, fee, out
}

// Protection 是用户在签名时设定的滑点保护。
type Protection struct {
	// MinAmountOut 为绝对最低输出量（原始整数单位）；nil 表示不使用。
	MinAmountOut *big.Int
	// RefR0/RefR1 为签名时刻的储备快照（无操纵的公允基准）。
	// 与 MaxSlippageBps 配合使用；为 nil 时无法使用百分比保护。
	RefR0, RefR1 *big.Int
	// MaxSlippageBps 为相对公允报价可容忍的最大滑点（bps）。
	MaxSlippageBps uint64
}

// TradeReq 是一笔交易请求。
type TradeReq struct {
	Seq   int
	Actor string
	Side  Side
	// AmountIn 为支付数量（原始整数单位）。为 nil 时使用 EchoOutputOf
	// 指定的前一笔成交输出（模拟攻击者把建仓所得全额回投平仓）。
	AmountIn *big.Int
	// EchoOutputOf > 0 时，本笔输入等于指定序号成交的输出数量。
	EchoOutputOf int
	Time         time.Time
	Note         string
	Protection   *Protection
}

// StepResult 是一笔请求处理完毕后的完整记录（含被拒绝的请求）。
type StepResult struct {
	Seq                        int
	Actor, Note                string
	Time                       time.Time
	Side                       Side
	Status                     string
	RejectReason, RejectDetail string
	AmountIn, Fee, AmountOut   *big.Int
	BeforeR0, BeforeR1         *big.Int
	AfterR0, AfterR1           *big.Int
	ImpactBps, KGrowthBps      float64
	RefOut, MinOut             *big.Int // RefOut：无操纵公允报价；MinOut：生效的最低输出阈值
	SlipVsRefBps               *float64 // 相对公允报价的实际滑点（bps）
	Tag                        string   // 由检测阶段回填：front/victim/back
	SandwichIdx                int      // 由检测阶段回填；-1 表示不属于任何攻击三元组
}

// Execute 处理一笔交易：校验 → 报价 → 滑点保护 → 成交或拒绝。
// 拒绝（无论非法输入还是滑点保护）时池状态严格不变。
func (p *Pool) Execute(req TradeReq) StepResult {
	r := StepResult{
		Seq:         req.Seq,
		Actor:       req.Actor,
		Note:        req.Note,
		Time:        req.Time,
		Side:        req.Side,
		BeforeR0:    p.R0(),
		BeforeR1:    p.R1(),
		SandwichIdx: -1,
	}
	if req.AmountIn != nil {
		r.AmountIn = new(big.Int).Set(req.AmountIn)
	}
	r.AfterR0, r.AfterR1 = new(big.Int).Set(r.BeforeR0), new(big.Int).Set(r.BeforeR1)

	// 一、输入校验。
	if req.AmountIn == nil || req.AmountIn.Sign() <= 0 {
		r.Status = StatusRejectedInvalid
		r.RejectReason = ReasonInvalidInput
		r.RejectDetail = "amount must be greater than 0"
		r.AmountIn = nilToZero(req.AmountIn)
		r.Fee, r.AmountOut = big.NewInt(0), big.NewInt(0)
		return r
	}
	if req.Protection != nil && req.Protection.MaxSlippageBps > 0 &&
		(req.Protection.RefR0 == nil || req.Protection.RefR1 == nil) {
		r.Status = StatusRejectedInvalid
		r.RejectReason = ReasonInvalidInput
		r.RejectDetail = "percentage slippage protection requires a reserve snapshot (RefR0, RefR1)"
		r.Fee, r.AmountOut = big.NewInt(0), big.NewInt(0)
		return r
	}
	if req.Protection != nil && req.Protection.MaxSlippageBps > uint64(BpsWhole) {
		r.Status = StatusRejectedInvalid
		r.RejectReason = ReasonInvalidInput
		r.RejectDetail = "max slippage cannot exceed 10000 bps"
		r.Fee, r.AmountOut = big.NewInt(0), big.NewInt(0)
		return r
	}

	// 二、精确报价。
	q, err := p.Quote(req.Side, req.AmountIn)
	if err != nil {
		r.Status = StatusRejectedInvalid
		r.RejectReason = ReasonInvalidInput
		r.RejectDetail = err.Error()
		r.Fee, r.AmountOut = big.NewInt(0), big.NewInt(0)
		return r
	}
	r.Fee, r.AmountOut = q.Fee, q.AmountOut

	// 三、滑点保护：把绝对阈值与“快照储备 + 容忍 bps”推导出的阈值取最严格者。
	if pt := req.Protection; pt != nil {
		var effMin *big.Int
		if pt.RefR0 != nil && pt.RefR1 != nil {
			_, _, refOut := quoteAt(
				sideReserves(req.Side, pt.RefR0, pt.RefR1, true),
				sideReserves(req.Side, pt.RefR0, pt.RefR1, false),
				req.AmountIn, p.feeBps)
			r.RefOut = refOut
			if pt.MaxSlippageBps > 0 {
				// floor(refOut * (10000 - tol) / 10000)
				snap := new(big.Int).Mul(refOut, big.NewInt(BpsWhole-int64(pt.MaxSlippageBps)))
				snap.Div(snap, big.NewInt(BpsWhole))
				effMin = snap
			}
			// 相对公允报价的实际滑点：(refOut - out) / refOut * 10000（bps）。
			bps := ratBps(new(big.Rat).SetFrac(
				new(big.Int).Sub(refOut, q.AmountOut), refOut))
			r.SlipVsRefBps = &bps
		}
		if pt.MinAmountOut != nil {
			if effMin == nil || pt.MinAmountOut.Cmp(effMin) > 0 {
				effMin = new(big.Int).Set(pt.MinAmountOut)
			}
		}
		r.MinOut = effMin
		if effMin != nil && q.AmountOut.Cmp(effMin) < 0 {
			r.Status = StatusRejectedSlippage
			r.RejectReason = ReasonSlippageProtection
			r.RejectDetail = formatReject(q.AmountOut, r.RefOut, effMin, r.SlipVsRefBps, pt.MaxSlippageBps)
			return r // 池状态不变
		}
	}

	// 四、成交：输入侧储备增加含手续费的完整 amountIn。
	p.r0, p.r1 = q.NewR0, q.NewR1
	r.AfterR0, r.AfterR1 = p.R0(), p.R1()
	r.Status = StatusExecuted
	r.ImpactBps = priceMoveBps(r.BeforeR0, r.BeforeR1, r.AfterR0, r.AfterR1)
	r.KGrowthBps = ratBps(new(big.Rat).SetFrac(
		new(big.Int).Sub(p.K(), new(big.Int).Mul(r.BeforeR0, r.BeforeR1)),
		new(big.Int).Mul(r.BeforeR0, r.BeforeR1)))
	return r
}

func formatReject(out, refOut, minOut *big.Int, slip *float64, tolBps uint64) string {
	s := fmt.Sprintf("实际输出 %s 低于最低接收阈值 %s", out, minOut)
	if refOut != nil && slip != nil {
		s += fmt.Sprintf("；无操纵公允报价 %s，实际滑点 %.4f%%", refOut, *slip/100)
		if tolBps > 0 {
			s += fmt.Sprintf("，超过 %.2f%% 上限", float64(tolBps)/100)
		}
	}
	return s
}

// sideReserves 按方向选取输入/输出侧储备。
func sideReserves(side Side, r0, r1 *big.Int, wantIn bool) *big.Int {
	if side == SellToken0 {
		if wantIn {
			return r0
		}
		return r1
	}
	if wantIn {
		return r1
	}
	return r0
}

// priceMoveBps 计算现货价 p = r1/r0 的变动幅度（bps，带符号：卖 token0 为负、
// 买 token0 为正）。全程 big.Rat 精确有理运算，最后一步才转 float64。
func priceMoveBps(r0Before, r1Before, r0After, r1After *big.Int) float64 {
	// pAfter/pBefore = (r1a/r0a)/(r1b/r0b) = r1a*r0b / (r0a*r1b)
	ratio := new(big.Rat).SetFrac(
		new(big.Int).Mul(r1After, r0Before),
		new(big.Int).Mul(r0After, r1Before))
	ratio.Sub(ratio, big.NewRat(1, 1))
	return ratBps(ratio)
}

// ratBps 把一个有理比例（0.01 = 1%）换算成 bps（100）。
func ratBps(x *big.Rat) float64 {
	f, _ := new(big.Rat).Mul(x, big.NewRat(BpsWhole, 1)).Float64()
	return f
}

func nilToZero(x *big.Int) *big.Int {
	if x == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(x)
}
