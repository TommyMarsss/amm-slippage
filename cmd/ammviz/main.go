// Command ammviz runs a demo trade sequence (normal flow, a whale trade,
// a slippage-protected trade that gets rejected, and a sandwich attack)
// against a constant-product pool and writes a single self-contained
// static HTML report (no server, no third-party libraries).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"

	"github.com/TommyMarsss/amm-slippage/amm"
)

type report struct {
	InitialReserves [2]string   `json:"initialReserves"`
	FinalReserves   [2]string   `json:"finalReserves"`
	FeePct          float64     `json:"feePct"`
	Logs            []amm.TradeLog `json:"logs"`
	Sandwiches      []amm.Sandwich `json:"sandwiches"`
	KBefore         string      `json:"kBefore"`
	KAfter          string      `json:"kAfter"`
}

func bi(v int64) *big.Int { return big.NewInt(v) }

func main() {
	out := flag.String("o", "report.html", "output HTML file")
	flag.Parse()

	pool := amm.NewPool(bi(1_000_000), bi(1_000_000), 997, 1000) // 0.3% fee
	kBefore := pool.K()
	initX, initY := pool.ReserveX.String(), pool.ReserveY.String()

	// Alice quotes 30k X->Y up front and sets a 0.5% slippage tolerance.
	// Her trade only executes at position 10, after a whale has moved the
	// price — realistic stale-quote slippage rejection.
	aliceQuote, err := pool.Quote(true, bi(30_000))
	if err != nil {
		log.Fatal(err)
	}
	aliceMinOut := amm.MinOutFromBps(aliceQuote, 50)

	// Eve's sandwich: front-run 60k X->Y, victim alice 25k X->Y, then eve
	// dumps everything she received. Her back-run amount depends on the
	// state after trades 0..4, so simulate the prefix on a clone to size it.
	dry := pool.Clone()
	prefix := []amm.Trade{
		{Actor: "bob", XToY: true, AmountIn: bi(5_000)},
		{Actor: "carol", XToY: false, AmountIn: bi(3_000)},
		{Actor: "dave", XToY: true, AmountIn: bi(8_000)},
		{Actor: "alice", XToY: false, AmountIn: bi(2_000)},
		{Actor: "bob", XToY: true, AmountIn: bi(6_000)},
	}
	for _, t := range prefix {
		if _, err := dry.Swap(t.XToY, t.AmountIn, t.MinAmountOut); err != nil {
			log.Fatal(err)
		}
	}
	eveFrontOut, err := dry.Swap(true, bi(60_000), nil)
	if err != nil {
		log.Fatal(err)
	}

	trades := append([]amm.Trade{}, prefix...)
	trades = append(trades,
		amm.Trade{Actor: "eve", XToY: true, AmountIn: bi(60_000)},                                     // 5 front-run
		amm.Trade{Actor: "alice", XToY: true, AmountIn: bi(25_000)},                                    // 6 victim
		amm.Trade{Actor: "eve", XToY: false, AmountIn: new(big.Int).Set(eveFrontOut.AmountOut)},        // 7 back-run
		amm.Trade{Actor: "bob", XToY: false, AmountIn: bi(4_000)},                                      // 8
		amm.Trade{Actor: "whale", XToY: true, AmountIn: bi(200_000)},                                   // 9 big impact
		amm.Trade{Actor: "alice", XToY: true, AmountIn: bi(30_000), MinAmountOut: aliceMinOut},         // 10 rejected
		amm.Trade{Actor: "carol", XToY: false, AmountIn: bi(6_000)},                                    // 11
	)

	logs := amm.ExecuteSequence(pool, trades, 1.0)
	sandwiches := amm.DetectSandwiches(amm.NewPool(bi(1_000_000), bi(1_000_000), 997, 1000), trades, 10)

	rep := report{
		InitialReserves: [2]string{initX, initY},
		FinalReserves:   [2]string{pool.ReserveX.String(), pool.ReserveY.String()},
		FeePct:          0.3,
		Logs:            logs,
		Sandwiches:      sandwiches,
		KBefore:         kBefore.String(),
		KAfter:          pool.K().String(),
	}
	data, err := json.Marshal(rep)
	if err != nil {
		log.Fatal(err)
	}

	html := strings.Replace(pageTemplate, "/*__DATA__*/", string(data), 1)
	if err := os.WriteFile(*out, []byte(html), 0o644); err != nil {
		log.Fatal(err)
	}

	executed, rejected := 0, 0
	for _, l := range logs {
		if l.Executed {
			executed++
		} else {
			rejected++
		}
	}
	fmt.Printf("wrote %s: %d executed, %d rejected, %d sandwich(es) detected\n",
		*out, executed, rejected, len(sandwiches))
	for _, s := range sandwiches {
		fmt.Printf("  sandwich: %s front#%d -> victim %s #%d (loss %s) -> back#%d, attacker profit %s\n",
			s.Attacker, s.FrontIndex, s.Victim, s.VictimIndex, s.VictimLoss, s.BackIndex, s.AttackerProfit)
	}
}

const pageTemplate = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>AMM 交易序列分析报告</title>
<style>
  :root {
    --bg: #0f1420; --panel: #1a2233; --border: #2a3550; --text: #e6ebf5;
    --muted: #8b96ad; --blue: #4f8ff7; --red: #f2545b; --orange: #f5a03f;
    --purple: #b07ff7; --yellow: #f5d03f; --green: #3fcf8e;
  }
  * { box-sizing: border-box; }
  body { margin: 0; padding: 24px; background: var(--bg); color: var(--text);
         font: 14px/1.5 -apple-system, "Segoe UI", "PingFang SC", sans-serif; }
  h1 { font-size: 20px; margin: 0 0 4px; }
  .sub { color: var(--muted); margin-bottom: 20px; }
  .cards { display: flex; flex-wrap: wrap; gap: 12px; margin-bottom: 20px; }
  .card { background: var(--panel); border: 1px solid var(--border); border-radius: 8px;
          padding: 12px 16px; min-width: 170px; }
  .card .label { color: var(--muted); font-size: 12px; }
  .card .value { font-size: 18px; font-weight: 600; margin-top: 2px; word-break: break-all; }
  .panel { background: var(--panel); border: 1px solid var(--border); border-radius: 8px;
           padding: 16px; margin-bottom: 20px; }
  .panel h2 { font-size: 15px; margin: 0 0 12px; }
  svg { display: block; width: 100%; height: auto; }
  .legend { display: flex; flex-wrap: wrap; gap: 16px; margin-top: 10px; font-size: 12px; color: var(--muted); }
  .legend span::before { content: ""; display: inline-block; width: 10px; height: 10px;
                         border-radius: 50%; margin-right: 6px; background: var(--c); }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th, td { text-align: left; padding: 7px 10px; border-bottom: 1px solid var(--border); }
  th { color: var(--muted); font-weight: 500; font-size: 12px; }
  tr.rejected td { background: rgba(242,84,91,.10); }
  tr.attacker td { background: rgba(245,160,63,.10); }
  tr.victim td { background: rgba(176,127,247,.10); }
  .tag { display: inline-block; padding: 1px 8px; border-radius: 10px; font-size: 11px; }
  .tag.ok { background: rgba(63,207,142,.15); color: var(--green); }
  .tag.rej { background: rgba(242,84,91,.18); color: var(--red); }
  .tag.anom { background: rgba(245,208,63,.18); color: var(--yellow); margin-left: 4px; }
  .num { font-variant-numeric: tabular-nums; }
</style>
</head>
<body>
<h1>恒定乘积 AMM 交易序列分析</h1>
<div class="sub">x · y = k ｜ 手续费 0.3% 留存池内（k 单调不减）｜ 全部整数运算，无浮点漂移</div>
<div class="cards" id="cards"></div>
<div class="panel">
  <h2>价格曲线（Y/X 中间价，按交易序号）</h2>
  <svg id="chart" viewBox="0 0 920 340" role="img"></svg>
  <div class="legend">
    <span style="--c:var(--blue)">正常成交</span>
    <span style="--c:var(--red)">滑点保护拒绝</span>
    <span style="--c:var(--orange)">攻击者交易（前置/平仓）</span>
    <span style="--c:var(--purple)">三明治受害者</span>
    <span style="--c:var(--yellow)">价格异常跳变（&gt;1%）</span>
  </div>
</div>
<div class="panel">
  <h2>三明治攻击检测</h2>
  <div id="sandwiches"></div>
</div>
<div class="panel">
  <h2>交易明细</h2>
  <table id="tbl">
    <thead><tr>
      <th>#</th><th>账户</th><th>方向</th><th>输入</th><th>输出</th><th>价格冲击</th>
      <th>价格跳变</th><th>k（交易后）</th><th>状态</th>
    </tr></thead>
    <tbody></tbody>
  </table>
</div>
<script>
const DATA = /*__DATA__*/;

const fmt = n => Number(n).toLocaleString("en-US");
const pct = v => (v >= 0 ? "+" : "") + v.toFixed(2) + "%";

// ---- summary cards ----
const executed = DATA.logs.filter(l => l.executed).length;
const rejected = DATA.logs.length - executed;
const totalLoss = DATA.sandwiches.reduce((a, s) => a + Number(s.victimLoss), 0);
const cards = [
  ["初始储备", fmt(DATA.initialReserves[0]) + " X / " + fmt(DATA.initialReserves[1]) + " Y"],
  ["最终储备", fmt(DATA.finalReserves[0]) + " X / " + fmt(DATA.finalReserves[1]) + " Y"],
  ["成交 / 拒绝", executed + " / " + rejected],
  ["不变量 k 增长", "+" + fmt(BigInt(DATA.kAfter) - BigInt(DATA.kBefore)) + "（手续费留存）"],
  ["三明治攻击", DATA.sandwiches.length + " 起，受害者损失 " + fmt(totalLoss) + " Y"],
];
document.getElementById("cards").innerHTML = cards.map(c =>
  '<div class="card"><div class="label">' + c[0] + '</div><div class="value">' + c[1] + "</div></div>"
).join("");

// ---- sandwich markup sets ----
const attackerIdx = new Set(), victimIdx = new Set();
DATA.sandwiches.forEach(s => {
  attackerIdx.add(s.frontIndex); attackerIdx.add(s.backIndex); victimIdx.add(s.victimIndex);
});
document.getElementById("sandwiches").innerHTML = DATA.sandwiches.length
  ? DATA.sandwiches.map(s =>
      "<p>攻击者 <b>" + s.attacker + "</b>：前置交易 #" + s.frontIndex +
      " 推高价格 → 受害者 <b>" + s.victim + "</b> 交易 #" + s.victimIndex +
      " 少获 <b class='num'>" + fmt(s.victimLoss) + "</b> 输出 → 平仓 #" + s.backIndex +
      "，攻击者净利 <b class='num'>" + fmt(s.attackerProfit) + "</b> X。</p>").join("")
  : "<p>未检测到三明治攻击模式。</p>";

// ---- price chart ----
const svg = document.getElementById("chart");
const W = 920, H = 340, PL = 70, PR = 20, PT = 16, PB = 34;
const logs = DATA.logs;
const prices = [logs[0].priceBefore].concat(logs.map(l => l.priceAfter));
let lo = Math.min.apply(null, prices), hi = Math.max.apply(null, prices);
const pad = (hi - lo) * 0.08 || hi * 0.01; lo -= pad; hi += pad;
const X = i => PL + (i / (prices.length - 1)) * (W - PL - PR);
const Y = p => PT + (1 - (p - lo) / (hi - lo)) * (H - PT - PB);
const NS = "http://www.w3.org/2000/svg";
const el = (tag, attrs, parent) => {
  const e = document.createElementNS(NS, tag);
  for (const k in attrs) e.setAttribute(k, attrs[k]);
  (parent || svg).appendChild(e); return e;
};
// gridlines + y labels
for (let g = 0; g <= 4; g++) {
  const p = lo + (hi - lo) * g / 4, y = Y(p);
  el("line", {x1: PL, y1: y, x2: W - PR, y2: y, stroke: "#2a3550", "stroke-width": 1});
  const t = el("text", {x: PL - 8, y: y + 4, "text-anchor": "end", fill: "#8b96ad", "font-size": 11});
  t.textContent = p.toFixed(4);
}
// x labels (trade indices)
logs.forEach((l, i) => {
  const t = el("text", {x: X(i + 1), y: H - 12, "text-anchor": "middle", fill: "#8b96ad", "font-size": 10});
  t.textContent = "#" + i;
});
// sandwich shading
DATA.sandwiches.forEach(s => {
  el("rect", {x: X(s.frontIndex + 1), y: PT, width: X(s.backIndex + 1) - X(s.frontIndex + 1),
              height: H - PT - PB, fill: "rgba(245,160,63,.08)"});
});
// price polyline
el("polyline", {
  points: prices.map((p, i) => X(i) + "," + Y(p)).join(" "),
  fill: "none", stroke: "#4f8ff7", "stroke-width": 2,
});
// markers
logs.forEach((l, i) => {
  const cx = X(i + 1), cy = Y(l.priceAfter);
  let color = "#4f8ff7", r = 4;
  if (!l.executed) color = "#f2545b";
  else if (attackerIdx.has(i)) color = "#f5a03f";
  else if (victimIdx.has(i)) { color = "#b07ff7"; r = 6; }
  if (l.anomaly) el("circle", {cx, cy, r: 10, fill: "none", stroke: "#f5d03f", "stroke-width": 1.5});
  const c = el("circle", {cx, cy, r, fill: color, stroke: "#0f1420", "stroke-width": 1});
  const tip = el("title", {}, c);
  tip.textContent = "#" + i + " " + l.actor + " " + l.direction +
    (l.executed ? " 成交 " + fmt(l.amountOut) : " 被拒绝") +
    " | 价格 " + l.priceAfter.toFixed(6);
  if (!l.executed) { // red X overlay
    el("line", {x1: cx - 6, y1: cy - 6, x2: cx + 6, y2: cy + 6, stroke: "#f2545b", "stroke-width": 2});
    el("line", {x1: cx - 6, y1: cy + 6, x2: cx + 6, y2: cy - 6, stroke: "#f2545b", "stroke-width": 2});
  }
});

// ---- trade table ----
const tbody = document.querySelector("#tbl tbody");
logs.forEach(l => {
  const tr = document.createElement("tr");
  if (!l.executed) tr.className = "rejected";
  else if (attackerIdx.has(l.index)) tr.className = "attacker";
  else if (victimIdx.has(l.index)) tr.className = "victim";
  const status = l.executed
    ? '<span class="tag ok">成交</span>' + (l.anomaly ? '<span class="tag anom">异常跳变</span>' : "")
    : '<span class="tag rej">拒绝</span> ' + (l.rejectReason || "");
  tr.innerHTML = "<td>" + l.index + "</td><td>" + l.actor + "</td><td>" + l.direction +
    "</td><td class='num'>" + fmt(l.amountIn) + "</td><td class='num'>" +
    (l.executed ? fmt(l.amountOut) : "—") + "</td><td class='num'>" +
    (l.executed ? l.priceImpactPct.toFixed(2) + "%" : "—") + "</td><td class='num'>" +
    (l.executed ? pct(l.priceJumpPct) : "—") + "</td><td class='num'>" + fmt(l.kAfter) +
    "</td><td>" + status + "</td>";
  tbody.appendChild(tr);
});
</script>
</body>
</html>
`
