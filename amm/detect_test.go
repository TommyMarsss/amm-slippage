package amm

import (
	"math/big"
	"testing"
)

func successSpec() ScenarioSpec {
	return ScenarioSpec{
		ID:      "sw-ok",
		Token0:  TokenMeta{Symbol: "WETH", Decimals: 18},
		Token1:  TokenMeta{Symbol: "USDC", Decimals: 6},
		StartR0: e18(100), StartR1: e6(300000), FeeBps: 30,
		Requests: []TradeReq{
			{Seq: 1, Actor: "Mallory", Side: SellToken0, AmountIn: e18(20), Time: t0(11, 0, 0)},
			{Seq: 2, Actor: "Victor", Side: SellToken0, AmountIn: e18(15), Time: t0(11, 0, 2),
				Protection: &Protection{RefR0: e18(100), RefR1: e6(300000), MaxSlippageBps: 10000}},
			{Seq: 3, Actor: "Mallory", Side: SellToken1, EchoOutputOf: 1, Time: t0(11, 0, 4)},
			{Seq: 4, Actor: "Alice", Side: SellToken0, AmountIn: e18(1), Time: t0(11, 10, 0)},
		},
	}
}

// TestSandwichVictimLoss 受害者损失必须等于“建仓前储备上的无攻击公允报价
// 减去实际输出”，数值用独立 big.Int 公式核对。
func TestSandwichVictimLoss(t *testing.T) {
	res := RunScenario(successSpec())
	if len(res.Sandwiches) != 1 {
		t.Fatalf("应检测到 1 次三明治，got %d", len(res.Sandwiches))
	}
	sw := res.Sandwiches[0]
	if sw.Attacker != "Mallory" || sw.Victim != "Victor" {
		t.Fatalf("攻击主体错误: %s -> %s", sw.Attacker, sw.Victim)
	}
	if sw.VictimRejected {
		t.Fatal("本例受害者应成交")
	}

	// 独立核算公允报价：victim 在 100/300000 储备上卖 15 WETH。
	net := new(big.Int).Div(new(big.Int).Mul(e18(15), big.NewInt(9970)), big.NewInt(10000))
	fair := new(big.Int).Div(new(big.Int).Mul(net, e6(300000)), new(big.Int).Add(e18(100), net))
	if sw.VictimFairOutRaw != fair.String() {
		t.Fatalf("公允输出 %s != 独立计算 %s", sw.VictimFairOutRaw, fair)
	}

	victim := res.Steps[1]
	wantLoss := new(big.Int).Sub(fair, victim.AmountOut)
	gotLoss := new(big.Int).Sub(fair, bi(sw.VictimFairOutRaw)) // sanity
	_ = gotLoss
	approx(t, "victim loss human (USDC)", sw.VictimLossHuman,
		float64(wantLoss.Int64())/1e6, 1e-9)
	// 参考报告的精确数值：39028.315427 - 27717.537773 = 11310.777654 USDC。
	approx(t, "victim loss reference", sw.VictimLossHuman, 11310.777654, 1e-6)
	approx(t, "victim loss bps", sw.VictimLossBps, 2898.0952752511, 1e-6)
}

// TestSandwichAttackerNet 攻击者净利 = 平仓收回 WETH − 建仓投入 WETH，
// 两腿手续费已包含在精确报价内。
func TestSandwichAttackerNet(t *testing.T) {
	res := RunScenario(successSpec())
	sw := res.Sandwiches[0]
	front, back := res.Steps[0], res.Steps[2]
	wantNet := new(big.Int).Sub(back.AmountOut, front.AmountIn)
	if wantNet.Sign() <= 0 {
		t.Fatalf("得手场景攻击者净利应为正，got %s", wantNet)
	}
	netHuman, _ := new(big.Rat).SetFrac(wantNet, e18(1)).Float64()
	approx(t, "attacker net human", sw.AttackerNetHuman, netHuman, 1e-12)
	approx(t, "attacker net reference", sw.AttackerNetHuman, 4.667795596503236, 1e-9)

	// 平仓输入必须恰好等于建仓输出（EchoOutputOf 全额回投）。
	if back.AmountIn.Cmp(front.AmountOut) != 0 {
		t.Fatalf("平仓输入 %s != 建仓输出 %s", back.AmountIn, front.AmountOut)
	}
}

// TestSandwichBlocked 受害者 50bps 严格保护时：交易被拒、零损失，攻击者在无
// 受害者接盘下自行平仓并净亏损；启发式仍识别为攻击（未遂）。
func TestSandwichBlocked(t *testing.T) {
	spec := successSpec()
	spec.ID = "sw-blocked"
	spec.Requests[1].Actor = "Vicky"
	spec.Requests[1].Protection = &Protection{RefR0: e18(100), RefR1: e6(300000), MaxSlippageBps: 50}
	res := RunScenario(spec)

	victim := res.Steps[1]
	if victim.Status != StatusRejectedSlippage {
		t.Fatalf("受害者应被拒绝，got %s", victim.Status)
	}
	// 拒绝后池储备必须仍是攻击者建仓后的状态。
	if res.Steps[1].AfterR0.Cmp(res.Steps[0].AfterR0) != 0 ||
		res.Steps[1].AfterR1.Cmp(res.Steps[0].AfterR1) != 0 {
		t.Fatal("受害者被拒后池状态不应变化")
	}
	if len(res.Sandwiches) != 1 {
		t.Fatalf("应仍检测到 1 次攻击（未遂），got %d", len(res.Sandwiches))
	}
	sw := res.Sandwiches[0]
	if !sw.VictimRejected || !sw.HeuristicDetected {
		t.Fatalf("应标记为已拦截且启发式确认，got %+v", sw)
	}
	if sw.VictimLossHuman != 0 {
		t.Fatalf("被拦截时受害者损失应为 0，got %f", sw.VictimLossHuman)
	}
	if sw.AttackerNetHuman >= 0 {
		t.Fatalf("无接盘平仓攻击者应净亏，got %.6f", sw.AttackerNetHuman)
	}
	approx(t, "attacker net (blocked)", sw.AttackerNetHuman, -0.0999498003673, 1e-9)
}

// TestHeuristicNoLabels 启发式不得依赖 Tag：重放无标签序列仍能识别，
// 且正常往返交易（冲击不足或窗口超限）不会被误报。
func TestHeuristicNoLabels(t *testing.T) {
	res := RunScenario(successSpec())
	if len(res.Patterns) != 1 {
		t.Fatalf("patterns = %d, want 1", len(res.Patterns))
	}
	p := res.Patterns[0]
	if p.FrontSeq != 1 || p.VictimSeq != 2 || p.BackSeq != 3 || p.Actor != "Mallory" {
		t.Fatalf("形态三元组错误: %+v", p)
	}
	if p.WindowSec != 4 {
		t.Fatalf("窗口 = %f, want 4", p.WindowSec)
	}
	if res.Steps[0].Tag != "front" || res.Steps[1].Tag != "victim" || res.Steps[2].Tag != "back" {
		t.Fatalf("step tag 回填错误: %q/%q/%q", res.Steps[0].Tag, res.Steps[1].Tag, res.Steps[2].Tag)
	}

	// 无攻击对照：Alice 的两笔小额反向交易（冲击 <10%）不得被识别。
	benign := ScenarioSpec{
		ID: "benign", Token0: TokenMeta{Symbol: "T0"}, Token1: TokenMeta{Symbol: "T1"},
		StartR0: big.NewInt(1_000_000), StartR1: big.NewInt(1_000_000), FeeBps: 30,
		Requests: []TradeReq{
			{Seq: 1, Actor: "Alice", Side: SellToken0, AmountIn: big.NewInt(100), Time: t0(9, 0, 0)},
			{Seq: 2, Actor: "Bob", Side: SellToken0, AmountIn: big.NewInt(100), Time: t0(9, 0, 1)},
			{Seq: 3, Actor: "Alice", Side: SellToken1, AmountIn: big.NewInt(100), Time: t0(9, 0, 2)},
		},
	}
	bres := RunScenario(benign)
	if len(bres.Sandwiches) != 0 || len(bres.Patterns) != 0 {
		t.Fatalf("小额正常交易不应触发三明治检测: %d/%d", len(bres.Sandwiches), len(bres.Patterns))
	}

	// 窗口超限对照：平仓腿在 5 分钟之外不得识别。
	late := successSpec()
	late.Requests[2].Time = t0(11, 6, 0)
	lres := RunScenario(late)
	if len(lres.Sandwiches) != 0 {
		t.Fatalf("超出时间窗不应识别为三明治，got %d", len(lres.Sandwiches))
	}
}

// TestJumpDetection 异常跳变检测：35% 急跌与随后的反向急涨应被记录，
// 被拒绝步骤不产生跳变。
func TestJumpDetection(t *testing.T) {
	res := RunScenario(successSpec())
	// #2 受害者成交 -2096bps（#1→#2）与 #3 平仓 +4979bps（#2→#3）越过阈值；
	// 序列首笔 #1 没有前价可比，#4 仅 -178bps。共 2 次跳变。
	if len(res.Jumps) != 2 {
		t.Fatalf("jumps = %d, want 2", len(res.Jumps))
	}
	if res.Jumps[0].FromSeq != 1 || res.Jumps[0].ToSeq != 2 {
		t.Fatalf("第一条跳变错误: %+v", res.Jumps[0])
	}
	if res.Jumps[len(res.Jumps)-1].Up != true {
		t.Fatal("平仓急涨应标记 up=true")
	}

	blocked := successSpec()
	blocked.Requests[1].Protection = &Protection{RefR0: e18(100), RefR1: e6(300000), MaxSlippageBps: 50}
	bres := RunScenario(blocked)
	// 受害者被拒、价格不变：只剩 #1 -> #3 一次跳变。
	if len(bres.Jumps) != 1 || bres.Jumps[0].FromSeq != 1 || bres.Jumps[0].ToSeq != 3 {
		t.Fatalf("被拒步骤不应产生跳变，got %+v", bres.Jumps)
	}
}

// TestSandwichRoundTripFeeConsistency 三笔交易后 k 依然单调不降，
// 攻击者利润来自受害者损失而非不变量违背。
func TestSandwichRoundTripFeeConsistency(t *testing.T) {
	res := RunScenario(successSpec())
	k := new(big.Int).Mul(res.Spec.StartR0, res.Spec.StartR1)
	for _, s := range res.Steps {
		if s.Status != StatusExecuted {
			continue
		}
		kn := new(big.Int).Mul(s.AfterR0, s.AfterR1)
		if kn.Cmp(k) < 0 {
			t.Fatalf("#%d 后 k 下降", s.Seq)
		}
		k = kn
	}
}
