package amm

import (
	"math/big"
	"testing"
	"time"
)

func bi(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad big int: " + s)
	}
	return v
}

func e18(n int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), bi("1000000000000000000"))
}

func e6(n int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), bi("1000000"))
}

func t0(h, m, s int) time.Time {
	return time.Date(2026, 10, 4, h, m, s, 0, time.UTC)
}

func approx(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	d := got - want
	if d < 0 {
		d = -d
	}
	if d > tol {
		t.Fatalf("%s = %.10f, want %.10f (±%.0e)", name, got, want, tol)
	}
}

// TestQuoteKnownVectors 用 Uniswap v2 公式的已知输出向量锁定报价：
// 100 WETH / 300,000 USDC 池，fee=0.30%。
func TestQuoteKnownVectors(t *testing.T) {
	p := NewPool(e18(100), e6(300000), 30)

	q, err := p.Quote(SellToken0, e18(1))
	if err != nil {
		t.Fatal(err)
	}
	if q.AmountOut.Cmp(bi("2961474103")) != 0 {
		t.Fatalf("1 WETH -> got %s, want 2961474103 USDC units", q.AmountOut)
	}
	if q.Fee.Cmp(bi("3000000000000000")) != 0 { // 0.003 WETH
		t.Fatalf("fee = %s, want 3e15", q.Fee)
	}
	if q.AmountInNet.Cmp(bi("997000000000000000")) != 0 {
		t.Fatalf("net = %s, want 0.997 WETH", q.AmountInNet)
	}

	// 25 WETH 巨鲸卖单（新池 100/300000 上的首笔交易，约换出 59,855.9 USDC）。
	q25, err := p.Quote(SellToken0, e18(25))
	if err != nil {
		t.Fatal(err)
	}
	if q25.AmountOut.Cmp(bi("59855913548")) != 0 {
		t.Fatalf("25 WETH -> got %s, want 59855913548", q25.AmountOut)
	}
}

// TestQuoteFormulaIndependent 用测试内独立实现的 big.Int 公式核对引擎输出，
// 覆盖正、反两个方向。
func TestQuoteFormulaIndependent(t *testing.T) {
	r0, r1 := e18(123), e6(456789)
	p := NewPool(r0, r1, 30)
	for _, side := range []Side{SellToken0, SellToken1} {
		in := e18(7)
		if side == SellToken1 {
			in = e6(9000)
		}
		q, err := p.Quote(side, in)
		if err != nil {
			t.Fatal(err)
		}
		var rIn, rOut *big.Int
		if side == SellToken0 {
			rIn, rOut = r0, r1
		} else {
			rIn, rOut = r1, r0
		}
		net := new(big.Int).Div(new(big.Int).Mul(in, big.NewInt(9970)), big.NewInt(10000))
		want := new(big.Int).Div(new(big.Int).Mul(net, rOut), new(big.Int).Add(rIn, net))
		if q.AmountOut.Cmp(want) != 0 {
			t.Fatalf("side %v: out %s != formula %s", side, q.AmountOut, want)
		}
	}
}

// TestInvariantZeroFeeExact 零费率且整除的交易序列必须严格保持 k 不变。
func TestInvariantZeroFeeExact(t *testing.T) {
	// r0=1000, r1=2048：每次投入全部 r0，恰好换走一半 r1（连续整除 4 次）。
	p := NewPool(big.NewInt(1000), big.NewInt(2048), 0)
	k0 := p.K()
	for i := 0; i < 4; i++ {
		r := p.Execute(TradeReq{Seq: i + 1, Side: SellToken0, AmountIn: p.R0()})
		if r.Status != StatusExecuted {
			t.Fatalf("trade %d: %s", i+1, r.RejectDetail)
		}
		if p.K().Cmp(k0) != 0 {
			t.Fatalf("零费率整除交易后 k 漂移: %s -> %s", k0, p.K())
		}
	}
}

// TestInvariantNonDecreasing 是核心不变量测试：任意（含手续费、含取整）交易
// 后 k 严格不减；连续 2000 笔小额交易也不得出现系统性偏离。
func TestInvariantNonDecreasing(t *testing.T) {
	// 单位储备池：每笔投入 100（占储备万分之一），净额/输出均触发整数取整。
	p := NewPool(big.NewInt(1_000_000), big.NewInt(1_000_000), 30)
	kPrev := p.K()
	for i := 0; i < 2000; i++ {
		r := p.Execute(TradeReq{Seq: i + 1, Actor: "dust", Side: SellToken0, AmountIn: big.NewInt(100)})
		if r.Status != StatusExecuted {
			t.Fatalf("dust trade %d rejected: %s", i+1, r.RejectDetail)
		}
		if p.K().Cmp(kPrev) < 0 {
			t.Fatalf("第 %d 笔后 k 下降：%s -> %s", i+1, kPrev, p.K())
		}
		kPrev = p.K()
	}

	// 含手续费时 k 必须严格增长（手续费为正）。
	pf := NewPool(e18(100), e6(300000), 30)
	kBefore := pf.K()
	rf := pf.Execute(TradeReq{Seq: 1, Side: SellToken0, AmountIn: e18(1)})
	if rf.Status != StatusExecuted || pf.K().Cmp(kBefore) <= 0 {
		t.Fatalf("含手续费成交后 k 未严格增长: %s -> %s", kBefore, pf.K())
	}
}

// TestImpactMatchesTheory 价格冲击必须与独立的有理公式
// (p1/p0 - 1) = r1'·r0/(r0'·r1) - 1 完全一致（仅在转 float64 时损失末位精度）。
func TestImpactMatchesTheory(t *testing.T) {
	p := NewPool(e18(100), e6(300000), 30)
	r := p.Execute(TradeReq{Seq: 1, Side: SellToken0, AmountIn: e18(25)})
	if r.Status != StatusExecuted {
		t.Fatal(r.RejectDetail)
	}
	ratio := new(big.Rat).SetFrac(
		new(big.Int).Mul(r.AfterR1, r.BeforeR0),
		new(big.Int).Mul(r.AfterR0, r.BeforeR1))
	want, _ := new(big.Rat).Sub(ratio, big.NewRat(1, 1)).Float64()
	want *= 10000
	approx(t, "25 WETH impact bps", r.ImpactBps, want, 1e-6)
	// 新池首笔 25 WETH：r0 100→125、r1 300000→约 240144，价格 3000→约 1921.15。
	approx(t, "25 WETH impact bps (参考值)", r.ImpactBps, -3596.1576946133, 1e-4)

	// 反方向交易冲击为正。
	r2 := p.Execute(TradeReq{Seq: 2, Side: SellToken1, AmountIn: e6(100000)})
	if r2.ImpactBps <= 0 {
		t.Fatalf("买入 token0 的冲击应为正，got %.4f bps", r2.ImpactBps)
	}
}

// TestSlippageAbsolute 绝对 minAmountOut：等于报价可成交，高出 1 单位即拒绝，
// 拒绝后池状态严格不变。
func TestSlippageAbsolute(t *testing.T) {
	p := NewPool(e18(100), e6(300000), 30)
	q, _ := p.Quote(SellToken0, e18(1))
	r0, r1 := p.R0(), p.R1()

	ok := p.Execute(TradeReq{Seq: 1, Side: SellToken0, AmountIn: e18(1),
		Protection: &Protection{MinAmountOut: new(big.Int).Set(q.AmountOut)}})
	if ok.Status != StatusExecuted {
		t.Fatalf("阈值等于实际输出时应成交，got %s", ok.Status)
	}

	p2 := NewPool(e18(100), e6(300000), 30)
	rej := p2.Execute(TradeReq{Seq: 1, Side: SellToken0, AmountIn: e18(1),
		Protection: &Protection{MinAmountOut: new(big.Int).Add(q.AmountOut, big.NewInt(1))}})
	if rej.Status != StatusRejectedSlippage || rej.RejectReason != ReasonSlippageProtection {
		t.Fatalf("应触发滑点拒绝，got status=%s reason=%s", rej.Status, rej.RejectReason)
	}
	if p2.R0().Cmp(r0) != 0 || p2.R1().Cmp(r1) != 0 {
		t.Fatalf("拒绝后池状态发生变化: %s/%s vs %s/%s", p2.R0(), p2.R1(), r0, r1)
	}
}

// TestSlippagePercentage 百分比保护：签名快照 + 50bps，在被巨鲸操纵后的储备
// 上必须拒绝；公允报价按快照储备独立重算。
func TestSlippagePercentage(t *testing.T) {
	snap0, snap1 := e18(100), e6(300000)
	p := NewPool(snap0, snap1, 30)
	whale := p.Execute(TradeReq{Seq: 1, Actor: "whale", Side: SellToken0, AmountIn: e18(25)})
	if whale.Status != StatusExecuted {
		t.Fatal(whale.RejectDetail)
	}
	before0, before1 := p.R0(), p.R1()
	victim := p.Execute(TradeReq{
		Seq: 2, Actor: "victim", Side: SellToken0, AmountIn: e18(8),
		Protection: &Protection{RefR0: snap0, RefR1: snap1, MaxSlippageBps: 50},
	})
	if victim.Status != StatusRejectedSlippage {
		t.Fatalf("受害者应被滑点保护拒绝，got %s", victim.Status)
	}
	if p.R0().Cmp(before0) != 0 || p.R1().Cmp(before1) != 0 {
		t.Fatal("拒绝后池状态发生变化")
	}
	if victim.SlipVsRefBps == nil || *victim.SlipVsRefBps < 3000 {
		t.Fatalf("相对公允报价滑点异常: %v", victim.SlipVsRefBps)
	}
	// 独立核算公允报价：8 WETH 在 100/300000 快照上的 0.3% 费报价。
	net := new(big.Int).Div(new(big.Int).Mul(e18(8), big.NewInt(9970)), big.NewInt(10000))
	fair := new(big.Int).Div(new(big.Int).Mul(net, snap1), new(big.Int).Add(snap0, net))
	if victim.RefOut.Cmp(fair) != 0 {
		t.Fatalf("公允报价 %s != 独立计算 %s", victim.RefOut, fair)
	}
	// 阈值 = floor(fair * 9950/10000)。
	wantMin := new(big.Int).Div(new(big.Int).Mul(fair, big.NewInt(9950)), big.NewInt(10000))
	if victim.MinOut.Cmp(wantMin) != 0 {
		t.Fatalf("阈值 %s != %s", victim.MinOut, wantMin)
	}

	// 未设保护的同一请求必须成交（对照：保护不是强制的）。
	noProt := p.Execute(TradeReq{Seq: 3, Actor: "careless", Side: SellToken0, AmountIn: e18(8)})
	if noProt.Status != StatusExecuted {
		t.Fatalf("无保护交易应成交，got %s", noProt.Status)
	}
}

// TestInvalidInputs 非法请求被明确拒绝且池状态不变。
func TestInvalidInputs(t *testing.T) {
	p := NewPool(e18(100), e6(300000), 30)
	r0, r1 := p.R0(), p.R1()
	for _, in := range []*big.Int{nil, big.NewInt(0), new(big.Int).Neg(big.NewInt(1))} {
		r := p.Execute(TradeReq{Seq: 1, Side: SellToken0, AmountIn: in})
		if r.Status != StatusRejectedInvalid || r.RejectReason != ReasonInvalidInput {
			t.Fatalf("输入 %v 应判非法，got %s/%s", in, r.Status, r.RejectReason)
		}
	}
	// 百分比保护缺少储备快照同样判非法。
	r := p.Execute(TradeReq{Seq: 2, Side: SellToken0, AmountIn: e18(1),
		Protection: &Protection{MaxSlippageBps: 50}})
	if r.Status != StatusRejectedInvalid {
		t.Fatalf("缺少快照应判非法，got %s", r.Status)
	}
	if p.R0().Cmp(r0) != 0 || p.R1().Cmp(r1) != 0 {
		t.Fatal("非法请求后池状态发生变化")
	}
}

// TestQuoteDoesNotMutate 报价不得改变池状态。
func TestQuoteDoesNotMutate(t *testing.T) {
	p := NewPool(e18(100), e6(300000), 30)
	r0, r1, k := p.R0(), p.R1(), p.K()
	for i := 0; i < 10; i++ {
		if _, err := p.Quote(SellToken0, e18(1)); err != nil {
			t.Fatal(err)
		}
	}
	if p.R0().Cmp(r0) != 0 || p.R1().Cmp(r1) != 0 || p.K().Cmp(k) != 0 {
		t.Fatal("Quote 意外修改了池状态")
	}
}
