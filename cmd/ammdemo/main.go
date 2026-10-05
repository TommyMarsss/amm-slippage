// Command ammdemo 运行三组 AMM 交易序列场景（连续交易/滑点拦截、三明治攻击
// 得手、三明治攻击被挫败），在终端打印关键校验结果，并生成单一静态 HTML
// 报告（默认 report.html，数据与逻辑全部内嵌、无外部依赖）。
package main

import (
	"flag"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/TommyMarsss/amm-slippage/amm"
)

var (
	weth = amm.TokenMeta{Symbol: "WETH", Decimals: 18}
	usdc = amm.TokenMeta{Symbol: "USDC", Decimals: 6}
)

func eth(x int64) *big.Int {
	r := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	return r.Mul(r, big.NewInt(x))
}

// ethFloat 支持小数 WETH 数量（如 0.5）。
func ethFloat(whole string, frac string) *big.Int {
	if len(frac) > 18 {
		panic("too many decimals")
	}
	b := make([]byte, 18-len(frac))
	for i := range b {
		b[i] = '0'
	}
	r, ok := new(big.Int).SetString(whole+frac+string(b), 10)
	if !ok {
		panic("bad eth amount")
	}
	return r
}

func usdcV(x int64) *big.Int {
	r := new(big.Int).Exp(big.NewInt(10), big.NewInt(6), nil)
	return r.Mul(r, big.NewInt(x))
}

func utc(h, m, s int) time.Time {
	return time.Date(2026, 10, 4, h, m, s, 0, time.UTC)
}

func startPool() (r0, r1 *big.Int) { return eth(100), usdcV(300000) }

func main() {
	out := flag.String("o", "report.html", "输出的静态 HTML 报告路径")
	flag.Parse()

	r0, r1 := startPool()
	results := []*amm.ScenarioResult{
		amm.RunScenario(normalScenario(r0, r1)),
		amm.RunScenario(sandwichSuccessScenario()),
		amm.RunScenario(sandwichBlockedScenario(r0, r1)),
	}

	rep := amm.BuildReport(results, 30)
	if err := amm.WriteReport(*out, rep); err != nil {
		fmt.Fprintln(os.Stderr, "write report:", err)
		os.Exit(1)
	}
	for _, sc := range results {
		printConsole(sc)
	}
	fmt.Printf("\n静态报告已生成：%s（直接用浏览器打开即可，无需启动服务）\n", *out)
}

// normalScenario 覆盖：小额交易 k 值单调不降、巨鲸价格冲击、滑点保护拒绝、
// 未设保护者在劣价成交、非法输入拒绝。
func normalScenario(r0, r1 *big.Int) amm.ScenarioSpec {
	return amm.ScenarioSpec{
		ID:          "normal",
		Name:        "场景一：连续交易、价格冲击与滑点保护拦截",
		Description: "100 WETH / 300,000 USDC 初始池。小额交易展示手续费计入 k 值后不变量单调不降；巨鲸单笔 25 WETH 造成大额价格冲击；随后 Dave 的限价单（容忍 0.5% 滑点）被正确拒绝，而未设保护的 Frank 在同一劣价成交。",
		Token0:      weth,
		Token1:      usdc,
		StartR0:     r0,
		StartR1:     r1,
		FeeBps:      30,
		Requests: []amm.TradeReq{
			{Seq: 1, Actor: "Alice", Side: amm.SellToken0, AmountIn: eth(1), Time: utc(9, 0, 0),
				Note: "小额卖单：手续费全额留存池内，k 值只增不减"},
			{Seq: 2, Actor: "Bob", Side: amm.SellToken0, AmountIn: eth(2), Time: utc(9, 0, 30),
				Note: "小额卖单"},
			{Seq: 3, Actor: "Eve（巨鲸）", Side: amm.SellToken0, AmountIn: eth(25), Time: utc(9, 1, 0),
				Note: "单笔 25 WETH 大额卖单，造成显著价格冲击"},
			{Seq: 4, Actor: "Dave", Side: amm.SellToken0, AmountIn: eth(8), Time: utc(9, 1, 30),
				Note:       "按盘前 3,000 价格签名，仅容忍 0.5% 滑点；巨鲸砸盘后理应被拒绝",
				Protection: &amm.Protection{RefR0: eth(100), RefR1: usdcV(300000), MaxSlippageBps: 50}},
			{Seq: 5, Actor: "Frank", Side: amm.SellToken0, AmountIn: eth(8), Time: utc(9, 2, 0),
				Note: "未设任何滑点保护，在同一劣价上成交，承担全部滑点"},
			{Seq: 6, Actor: "Buggy", Side: amm.SellToken0, AmountIn: big.NewInt(0), Time: utc(9, 2, 30),
				Note: "金额为 0 的非法请求"},
			{Seq: 7, Actor: "Alice", Side: amm.SellToken0, AmountIn: ethFloat("0", "5"), Time: utc(9, 3, 0),
				Note: "市场继续交易"},
		},
	}
}

// sandwichSuccessScenario：攻击者双腿夹击得手，受害者滑点容忍 100%。
func sandwichSuccessScenario() amm.ScenarioSpec {
	return amm.ScenarioSpec{
		ID:          "sandwich-ok",
		Name:        "场景二：典型三明治攻击得手",
		Description: "Mallory 在受害者 Victor 的卖单前后夹击：抢先把 WETH 砸低，受害者在劣价成交后攻击者再买回 WETH。卡片给出受害者相对无攻击公允报价的精确损失，以及攻击者两腿手续费扣除后的真实净利。",
		Token0:      weth,
		Token1:      usdc,
		StartR0:     eth(100),
		StartR1:     usdcV(300000),
		FeeBps:      30,
		Requests: []amm.TradeReq{
			{Seq: 1, Actor: "Mallory", Side: amm.SellToken0, AmountIn: eth(20), Time: utc(11, 0, 0),
				Note: "攻击者抢先：与受害者同向大额卖出 WETH，把 WETH 价格砸低"},
			{Seq: 2, Actor: "Victor", Side: amm.SellToken0, AmountIn: eth(15), Time: utc(11, 0, 2),
				Note:       "受害者在被扭曲的价格上卖出 15 WETH（滑点保护形同虚设：容忍 100%）",
				Protection: &amm.Protection{RefR0: eth(100), RefR1: usdcV(300000), MaxSlippageBps: 10000}},
			{Seq: 3, Actor: "Mallory", Side: amm.SellToken1, EchoOutputOf: 1, Time: utc(11, 0, 4),
				Note: "攻击者反向平仓：把建仓所得 USDC 全部回投，买回 WETH 赚取价差"},
			{Seq: 4, Actor: "Alice", Side: amm.SellToken0, AmountIn: eth(1), Time: utc(11, 10, 0),
				Note: "攻击窗口外的正常交易"},
			{Seq: 5, Actor: "Bob", Side: amm.SellToken1, AmountIn: usdcV(3000), Time: utc(11, 11, 0),
				Note: "攻击窗口外的正常买单（3,000 USDC）"},
		},
	}
}

// sandwichBlockedScenario：受害者 0.5% 严格容忍，攻击被挫败、攻击者净亏。
func sandwichBlockedScenario(r0, r1 *big.Int) amm.ScenarioSpec {
	return amm.ScenarioSpec{
		ID:          "sandwich-blocked",
		Name:        "场景三：三明治攻击被滑点保护挫败",
		Description: "受害者 Vicky 设置了 0.5% 的严格滑点容忍度：攻击者建仓后其交易被拒绝（✕），攻击者被迫在没有受害者接盘的情况下自行平仓，往返手续费使其净亏损。启发式检测仍能识别这次攻击未遂形态。",
		Token0:      weth,
		Token1:      usdc,
		StartR0:     r0,
		StartR1:     r1,
		FeeBps:      30,
		Requests: []amm.TradeReq{
			{Seq: 1, Actor: "Mallory", Side: amm.SellToken0, AmountIn: eth(20), Time: utc(13, 0, 0),
				Note: "攻击者抢先砸盘（同场景二）"},
			{Seq: 2, Actor: "Vicky", Side: amm.SellToken0, AmountIn: eth(15), Time: utc(13, 0, 2),
				Note:       "按盘前价格签名且只容忍 0.5% 滑点——交易被拒绝，池状态不变",
				Protection: &amm.Protection{RefR0: eth(100), RefR1: usdcV(300000), MaxSlippageBps: 50}},
			{Seq: 3, Actor: "Mallory", Side: amm.SellToken1, EchoOutputOf: 1, Time: utc(13, 0, 4),
				Note: "受害者没有成交，攻击者只能自行平仓，往返手续费构成净亏损"},
			{Seq: 4, Actor: "Alice", Side: amm.SellToken0, AmountIn: eth(1), Time: utc(13, 10, 0),
				Note: "攻击窗口外的正常交易"},
		},
	}
}

func printConsole(sc *amm.ScenarioResult) {
	fmt.Printf("==== %s ====\n", sc.Spec.Name)
	k0 := new(big.Int).Mul(sc.Spec.StartR0, sc.Spec.StartR1)
	kPrev := new(big.Int).Set(k0)
	for i := range sc.Steps {
		s := &sc.Steps[i]
		switch s.Status {
		case amm.StatusExecuted:
			k := new(big.Int).Mul(s.AfterR0, s.AfterR1)
			if k.Cmp(kPrev) < 0 {
				fmt.Printf("  !! 不变量违规：k 从 %s 降到 %s\n", kPrev, k)
			}
			kPrev.Set(k)
			fmt.Printf("  #%d %-10s 成交  out=%s  impact=%.2f bps  kGrowth=%.4f bps\n",
				s.Seq, s.Actor, s.AmountOut, s.ImpactBps, s.KGrowthBps)
		case amm.StatusRejectedSlippage:
			fmt.Printf("  #%d %-10s 拒绝（滑点保护，池状态不变）：%s\n", s.Seq, s.Actor, s.RejectDetail)
		default:
			fmt.Printf("  #%d %-10s 拒绝（非法输入）：%s\n", s.Seq, s.Actor, s.RejectDetail)
		}
	}
	for _, sw := range sc.Sandwiches {
		fmt.Printf("  [三明治] %s 夹 %s：受害者损失 %.6f %s（%.2f bps），攻击者净利 %.6f %s（被拦截=%v）\n",
			sw.Attacker, sw.Victim, sw.VictimLossHuman, sw.MidSymbol, sw.VictimLossBps,
			sw.AttackerNetHuman, sw.InSymbol, sw.VictimRejected)
	}
}
