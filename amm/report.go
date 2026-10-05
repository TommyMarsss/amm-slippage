package amm

import (
	_ "embed"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"time"
)

//go:embed template.html
var reportTemplate string

const dataPlaceholder = "__REPORT_DATA__"

// Report 是注入静态 HTML 的顶层数据结构。
type Report struct {
	GeneratedAt string           `json:"generatedAt"`
	FeeBps      uint64           `json:"feeBps"`
	FeePct      float64          `json:"feePct"`
	Scenarios   []ScenarioReport `json:"scenarios"`
}

// ScenarioReport 是单个场景的展示数据。
type ScenarioReport struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	Token0       string        `json:"token0"`
	Token1       string        `json:"token1"`
	Decimals0    uint          `json:"decimals0"`
	Decimals1    uint          `json:"decimals1"`
	StartR0Raw   string        `json:"startR0Raw"`
	StartR1Raw   string        `json:"startR1Raw"`
	StartR0Human float64       `json:"startR0Human"`
	StartR1Human float64       `json:"startR1Human"`
	StartPrice   float64       `json:"startPrice"`
	Steps        []StepReport  `json:"steps"`
	Sandwiches   []Sandwich    `json:"sandwiches"`
	Patterns     []Pattern     `json:"patterns"`
	Jumps        []Jump        `json:"jumps"`
	Summary      SummaryReport `json:"summary"`
}

// StepReport 是单笔交易的展示数据。指针字段在无保护/无异常时省略。
type StepReport struct {
	Seq            int      `json:"seq"`
	Actor          string   `json:"actor"`
	Side           string   `json:"side"`
	Note           string   `json:"note"`
	Time           string   `json:"time"`
	Status         string   `json:"status"`
	RejectReason   string   `json:"rejectReason,omitempty"`
	RejectDetail   string   `json:"rejectDetail,omitempty"`
	AmountInRaw    string   `json:"amountInRaw"`
	AmountInHuman  float64  `json:"amountInHuman"`
	InSymbol       string   `json:"inSymbol"`
	FeeHuman       float64  `json:"feeHuman"`
	AmountOutRaw   string   `json:"amountOutRaw"`
	AmountOutHuman float64  `json:"amountOutHuman"`
	OutSymbol      string   `json:"outSymbol"`
	R0Raw          string   `json:"r0Raw"`
	R1Raw          string   `json:"r1Raw"`
	R0Human        float64  `json:"r0Human"`
	R1Human        float64  `json:"r1Human"`
	Price          float64  `json:"price"`
	ImpactBps      float64  `json:"impactBps"`
	KGrowthBps     float64  `json:"kGrowthBps"`
	SlipVsRefBps   *float64 `json:"slipVsRefBps,omitempty"`
	MinOutHuman    *float64 `json:"minOutHuman,omitempty"`
	RefOutHuman    *float64 `json:"refOutHuman,omitempty"`
	Tag            string   `json:"tag,omitempty"`
	SandwichIdx    int      `json:"sandwichIdx"`
}

// SummaryReport 是场景级汇总。
type SummaryReport struct {
	TotalSteps      int     `json:"totalSteps"`
	Executed        int     `json:"executed"`
	RejectedSlip    int     `json:"rejectedSlip"`
	RejectedInvalid int     `json:"rejectedInvalid"`
	MaxImpactBps    float64 `json:"maxImpactBps"`
	TotalVictimLoss float64 `json:"totalVictimLoss"`
	AttackerNet     float64 `json:"attackerNet"`
	KGrowthTotalBps float64 `json:"kGrowthTotalBps"`
}

// BuildReport 把若干场景结果转换为报告数据。feeBps 用于顶层展示口径。
func BuildReport(results []*ScenarioResult, feeBps uint64) Report {
	rep := Report{
		GeneratedAt: time.Now().UTC().Format("2006-01-02 15:04:05 MST"),
		FeeBps:      feeBps,
		FeePct:      float64(feeBps) / 100,
	}
	for _, res := range results {
		rep.Scenarios = append(rep.Scenarios, buildScenario(res))
	}
	return rep
}

func buildScenario(res *ScenarioResult) ScenarioReport {
	spec := res.Spec
	sr := ScenarioReport{
		ID:           spec.ID,
		Name:         spec.Name,
		Description:  spec.Description,
		Token0:       spec.Token0.Symbol,
		Token1:       spec.Token1.Symbol,
		Decimals0:    spec.Token0.Decimals,
		Decimals1:    spec.Token1.Decimals,
		StartR0Raw:   spec.StartR0.String(),
		StartR1Raw:   spec.StartR1.String(),
		StartR0Human: HumanAmount(spec.StartR0, spec.Token0.Decimals),
		StartR1Human: HumanAmount(spec.StartR1, spec.Token1.Decimals),
		StartPrice:   PriceHuman(spec.StartR0, spec.StartR1, spec.Token0.Decimals, spec.Token1.Decimals),
		Sandwiches:   res.Sandwiches,
		Patterns:     res.Patterns,
		Jumps:        res.Jumps,
	}
	if sr.Sandwiches == nil {
		sr.Sandwiches = []Sandwich{}
	}
	if sr.Patterns == nil {
		sr.Patterns = []Pattern{}
	}
	if sr.Jumps == nil {
		sr.Jumps = []Jump{}
	}

	for i := range res.Steps {
		st := &res.Steps[i]
		inSym, outSym, inDec, outDec := sideSymbols(spec, st.Side)
		step := StepReport{
			Seq:            st.Seq,
			Actor:          st.Actor,
			Side:           st.Side.String(),
			Note:           st.Note,
			Time:           st.Time.UTC().Format("15:04:05"),
			Status:         st.Status,
			RejectReason:   st.RejectReason,
			RejectDetail:   st.RejectDetail,
			AmountInRaw:    rawString(st.AmountIn),
			AmountInHuman:  HumanAmount(st.AmountIn, inDec),
			InSymbol:       inSym,
			FeeHuman:       HumanAmount(st.Fee, inDec),
			AmountOutRaw:   rawString(st.AmountOut),
			AmountOutHuman: HumanAmount(st.AmountOut, outDec),
			OutSymbol:      outSym,
			R0Raw:          st.AfterR0.String(),
			R1Raw:          st.AfterR1.String(),
			R0Human:        HumanAmount(st.AfterR0, spec.Token0.Decimals),
			R1Human:        HumanAmount(st.AfterR1, spec.Token1.Decimals),
			Price:          res.StepPrice(*st),
			ImpactBps:      st.ImpactBps,
			KGrowthBps:     st.KGrowthBps,
			SlipVsRefBps:   st.SlipVsRefBps,
			Tag:            st.Tag,
			SandwichIdx:    st.SandwichIdx,
		}
		if st.MinOut != nil {
			m := HumanAmount(st.MinOut, outDec)
			step.MinOutHuman = &m
		}
		if st.RefOut != nil {
			rf := HumanAmount(st.RefOut, outDec)
			step.RefOutHuman = &rf
		}
		sr.Steps = append(sr.Steps, step)

		sr.Summary.TotalSteps++
		switch st.Status {
		case StatusExecuted:
			sr.Summary.Executed++
			if absF(st.ImpactBps) > sr.Summary.MaxImpactBps {
				sr.Summary.MaxImpactBps = absF(st.ImpactBps)
			}
			sr.Summary.KGrowthTotalBps += st.KGrowthBps
		case StatusRejectedSlippage:
			sr.Summary.RejectedSlip++
		case StatusRejectedInvalid:
			sr.Summary.RejectedInvalid++
		}
	}
	for _, sw := range res.Sandwiches {
		sr.Summary.AttackerNet += sw.AttackerNetHuman
		if !sw.VictimRejected {
			sr.Summary.TotalVictimLoss += sw.VictimLossHuman
		}
	}
	return sr
}

func sideSymbols(spec ScenarioSpec, side Side) (inSym, outSym string, inDec, outDec uint) {
	if side == SellToken0 {
		return spec.Token0.Symbol, spec.Token1.Symbol, spec.Token0.Decimals, spec.Token1.Decimals
	}
	return spec.Token1.Symbol, spec.Token0.Symbol, spec.Token1.Decimals, spec.Token0.Decimals
}

func rawString(x *big.Int) string {
	if x == nil {
		return "0"
	}
	return x.String()
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// WriteReport 把报告数据序列化后注入模板，写出单一静态 HTML 文件。
// json.Marshal 默认转义 <、>、&，因此数据可安全内嵌于 <script> 块。
func WriteReport(path string, rep Report) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	html := strings.Replace(reportTemplate, dataPlaceholder, string(data), 1)
	if strings.Contains(html, dataPlaceholder) {
		return os.ErrInvalid // 模板占位符缺失（不应发生）
	}
	return os.WriteFile(path, []byte(html), 0o644)
}
