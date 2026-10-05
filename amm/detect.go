package amm

import (
	"math"
	"math/big"
	"time"
)

// 检测参数（可按监控环境调整）。
const (
	// JumpThresholdBps：相邻成交间现货价变动 ≥ 10%（1000 bps）记为异常跳变。
	JumpThresholdBps = 1000.0
	// SandwichWindow：攻击者建仓与平仓两条腿必须落在 5 分钟窗口内。
	SandwichWindow = 5 * time.Minute
	// AttackLegMinImpactBps：建仓/平仓腿各自至少造成的价格冲击，10%。
	AttackLegMinImpactBps = 1000.0
)

// Jump 描述相邻两笔成交之间的短时价格异常跳变。
type Jump struct {
	FromSeq    int     `json:"fromSeq"`
	ToSeq      int     `json:"toSeq"`
	Actor      string  `json:"actor"`
	MoveBps    float64 `json:"moveBps"`
	Up         bool    `json:"up"`
	GapSeconds float64 `json:"gapSeconds"`
}

// Pattern 是不依赖预设标签、仅依据行为形态识别出的疑似三明治夹击。
type Pattern struct {
	FrontSeq  int     `json:"frontSeq"`
	VictimSeq int     `json:"victimSeq"`
	BackSeq   int     `json:"backSeq"`
	Actor     string  `json:"actor"`
	Victim    string  `json:"victim"`
	FrontBps  float64 `json:"frontBps"`
	BackBps   float64 `json:"backBps"`
	WindowSec float64 `json:"windowSec"`
}

// Sandwich 是一次三明治攻击的完整损益核算（得手或被挫败）。
type Sandwich struct {
	Attacker           string  `json:"attacker"`
	Victim             string  `json:"victim"`
	FrontInHuman       float64 `json:"frontInHuman"`
	FrontOutHuman      float64 `json:"frontOutHuman"`
	VictimInHuman      float64 `json:"victimInHuman"`
	VictimOutHuman     float64 `json:"victimOutHuman"`
	BackInHuman        float64 `json:"backInHuman"`
	BackOutHuman       float64 `json:"backOutHuman"`
	InSymbol           string  `json:"inSymbol"`
	MidSymbol          string  `json:"midSymbol"`
	VictimFairOutRaw   string  `json:"-"`
	VictimFairOutHuman float64 `json:"victimFairOutHuman"`
	VictimLossHuman    float64 `json:"victimLossHuman"`
	VictimLossBps      float64 `json:"victimLossBps"`
	AttackerNetHuman   float64 `json:"attackerNetHuman"`
	FrontSeq           int     `json:"frontSeq"`
	VictimSeq          int     `json:"victimSeq"`
	BackSeq            int     `json:"backSeq"`
	VictimRejected     bool    `json:"victimRejected"`
	HeuristicDetected  bool    `json:"heuristicDetected"`

	// 精确值（不序列化），供测试断言。
	frontIn, frontOut, victimIn, victimOut *big.Int
	backIn, backOut, victimFairOut         *big.Int
	attackerNet                            *big.Int
	victimLoss                             *big.Int
}

// Detect 在场景执行完毕后运行全部检测器，并回填 step 的 Tag/SandwichIdx。
func Detect(res *ScenarioResult) {
	detectJumps(res)
	detectSandwiches(res)
}

// detectJumps 比较相邻“已成交”步骤的现货价（被拒绝的步骤不改变价格，跳过）。
func detectJumps(res *ScenarioResult) {
	var prev *StepResult
	for i := range res.Steps {
		s := &res.Steps[i]
		if s.Status != StatusExecuted {
			continue
		}
		if prev != nil {
			// 被拒绝的步骤不改变价格；此处比较的是“上一笔成交价 → 本笔成交价”，
			// 数值上即本笔成交自身的价格冲击 s.ImpactBps（prev.After == s.Before）。
			move := s.ImpactBps
			if math.Abs(move) >= JumpThresholdBps {
				res.Jumps = append(res.Jumps, Jump{
					FromSeq:    prev.Seq,
					ToSeq:      s.Seq,
					Actor:      s.Actor,
					MoveBps:    math.Abs(move),
					Up:         move > 0,
					GapSeconds: s.Time.Sub(prev.Time).Seconds(),
				})
			}
		}
		prev = s
	}
}

// detectSandwiches 用纯行为启发式扫描三元组：
// 同一账户在时间窗内以相反方向、各造成至少 10% 价格冲击的两条大额腿，
// 首尾夹着另一个账户的订单。不读取任何预设标签。
func detectSandwiches(res *ScenarioResult) {
	n := len(res.Steps)
	consumed := make([]bool, n)

	for f := 0; f < n; f++ {
		if consumed[f] {
			continue
		}
		sf := &res.Steps[f]
		if sf.Status != StatusExecuted || sf.Actor == "" ||
			math.Abs(sf.ImpactBps) < AttackLegMinImpactBps {
			continue
		}
		for b := f + 2; b < n; b++ {
			if consumed[b] {
				continue
			}
			sb := &res.Steps[b]
			if sb.Status != StatusExecuted ||
				sb.Actor != sf.Actor || sb.Side == sf.Side ||
				math.Abs(sb.ImpactBps) < AttackLegMinImpactBps ||
				sb.Time.Sub(sf.Time) > SandwichWindow ||
				sb.Time.Before(sf.Time) {
				continue
			}
			// 在 (f,b) 之间寻找第一个属于其他账户的受害者订单（成交或被拒均可）。
			v := -1
			for k := f + 1; k < b; k++ {
				if consumed[k] {
					continue
				}
				sv := &res.Steps[k]
				if sv.Actor != "" && sv.Actor != sf.Actor {
					v = k
					break
				}
			}
			if v < 0 {
				continue
			}
			sw := buildSandwich(res, f, v, b)
			res.Sandwiches = append(res.Sandwiches, sw)
			res.Patterns = append(res.Patterns, Pattern{
				FrontSeq:  sf.Seq,
				VictimSeq: res.Steps[v].Seq,
				BackSeq:   sb.Seq,
				Actor:     sf.Actor,
				Victim:    res.Steps[v].Actor,
				FrontBps:  math.Abs(sf.ImpactBps),
				BackBps:   math.Abs(sb.ImpactBps),
				WindowSec: sb.Time.Sub(sf.Time).Seconds(),
			})
			sf.Tag, res.Steps[v].Tag, sb.Tag = "front", "victim", "back"
			sf.SandwichIdx, res.Steps[v].SandwichIdx, sb.SandwichIdx =
				len(res.Sandwiches)-1, len(res.Sandwiches)-1, len(res.Sandwiches)-1
			consumed[f], consumed[v], consumed[b] = true, true, true
			break // 该建仓腿已配对，继续扫描后续账户
		}
	}
}

// buildSandwich 用攻击者建仓前的储备作为“无操纵公允基准”精确核算损益。
func buildSandwich(res *ScenarioResult, f, v, b int) Sandwich {
	spec := res.Spec
	sf, sv, sb := &res.Steps[f], &res.Steps[v], &res.Steps[b]

	// 公允报价：若攻击者从未建仓，受害者在 sf.BeforeR0/R1 上成交会得到多少。
	rInFair, rOutFair := sf.BeforeR0, sf.BeforeR1
	if sv.Side == SellToken1 {
		rInFair, rOutFair = sf.BeforeR1, sf.BeforeR0
	}
	_, _, fairOut := quoteAt(rInFair, rOutFair, sv.AmountIn, spec.FeeBps)

	victimRejected := sv.Status == StatusRejectedSlippage || sv.Status == StatusRejectedInvalid
	var victimLoss *big.Int
	if !victimRejected {
		victimLoss = new(big.Int).Sub(fairOut, sv.AmountOut)
		if victimLoss.Sign() < 0 {
			victimLoss.SetInt64(0)
		}
	} else {
		victimLoss = big.NewInt(0)
	}
	lossBps := ratBps(new(big.Rat).SetFrac(
		new(big.Int).Sub(fairOut, outOrZero(sv)), fairOut))

	// 攻击者净利：平仓收回的建仓代币 − 建仓投入（两腿手续费已含在报价内）。
	net := new(big.Int).Sub(sb.AmountOut, sf.AmountIn)

	inSym, midSym := spec.Token0.Symbol, spec.Token1.Symbol
	if sf.Side == SellToken1 {
		inSym, midSym = spec.Token1.Symbol, spec.Token0.Symbol
	}
	dIn := decimalsOf(spec, sf.Side, true)
	dMid := decimalsOf(spec, sf.Side, false)

	return Sandwich{
		Attacker:           sf.Actor,
		Victim:             sv.Actor,
		FrontInHuman:       HumanAmount(sf.AmountIn, dIn),
		FrontOutHuman:      HumanAmount(sf.AmountOut, dMid),
		VictimInHuman:      HumanAmount(sv.AmountIn, dIn),
		VictimOutHuman:     HumanAmount(outOrZero(sv), dMid),
		BackInHuman:        HumanAmount(sb.AmountIn, dMid),
		BackOutHuman:       HumanAmount(sb.AmountOut, dIn),
		InSymbol:           inSym,
		MidSymbol:          midSym,
		VictimFairOutRaw:   fairOut.String(),
		VictimFairOutHuman: HumanAmount(fairOut, dMid),
		VictimLossHuman:    HumanAmount(victimLoss, dMid),
		VictimLossBps:      lossBps,
		AttackerNetHuman:   HumanAmount(net, dIn),
		FrontSeq:           sf.Seq,
		VictimSeq:          sv.Seq,
		BackSeq:            sb.Seq,
		VictimRejected:     victimRejected,
		HeuristicDetected:  true,
		frontIn:            new(big.Int).Set(sf.AmountIn),
		frontOut:           new(big.Int).Set(sf.AmountOut),
		victimIn:           nilToZero(sv.AmountIn),
		victimOut:          outOrZero(sv),
		backIn:             new(big.Int).Set(sb.AmountIn),
		backOut:            new(big.Int).Set(sb.AmountOut),
		victimFairOut:      fairOut,
		attackerNet:        net,
		victimLoss:         victimLoss,
	}
}

func outOrZero(s *StepResult) *big.Int {
	if s.AmountOut == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(s.AmountOut)
}

func decimalsOf(spec ScenarioSpec, side Side, inSide bool) uint {
	if side == SellToken0 {
		if inSide {
			return spec.Token0.Decimals
		}
		return spec.Token1.Decimals
	}
	if inSide {
		return spec.Token1.Decimals
	}
	return spec.Token0.Decimals
}
