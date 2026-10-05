package amm

import (
	"math/big"
	"testing"
)

// TestSandwichVictimLoss builds a canonical sandwich attack and verifies:
//  1. the victim receives strictly less than in the no-attack baseline,
//  2. DetectSandwiches reports the triple with the exact loss,
//  3. the attacker ends with a profit.
func TestSandwichVictimLoss(t *testing.T) {
	newPool := func() *Pool { return NewPool(bi(1_000_000), bi(1_000_000), 997, 1000) }

	// Baseline: victim trades alone.
	base := newPool()
	baseRes, err := base.Swap(true, bi(20_000), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Attack: eve front-runs, alice (victim) trades, eve back-runs.
	trades := []Trade{
		{Actor: "eve", XToY: true, AmountIn: bi(50_000)},
		{Actor: "alice", XToY: true, AmountIn: bi(20_000)},
		{Actor: "eve", XToY: false, AmountIn: nil}, // filled below with eve's Y output
	}
	attack := newPool()
	front, err := attack.Swap(true, trades[0].AmountIn, nil)
	if err != nil {
		t.Fatal(err)
	}
	victim, err := attack.Swap(true, trades[1].AmountIn, nil)
	if err != nil {
		t.Fatal(err)
	}
	trades[2].AmountIn = new(big.Int).Set(front.AmountOut) // eve dumps all Y
	back, err := attack.Swap(false, trades[2].AmountIn, nil)
	if err != nil {
		t.Fatal(err)
	}

	if victim.AmountOut.Cmp(baseRes.AmountOut) >= 0 {
		t.Fatalf("victim should get less under attack: %s vs baseline %s",
			victim.AmountOut, baseRes.AmountOut)
	}
	wantLoss := new(big.Int).Sub(baseRes.AmountOut, victim.AmountOut)

	profit := new(big.Int).Sub(back.AmountOut, trades[0].AmountIn)
	if profit.Sign() <= 0 {
		t.Fatalf("attacker should profit, got %s", profit)
	}

	// Detection must find exactly this triple with the exact loss.
	sandwiches := DetectSandwiches(newPool(), trades, 10)
	if len(sandwiches) != 1 {
		t.Fatalf("expected 1 sandwich, got %d: %+v", len(sandwiches), sandwiches)
	}
	s := sandwiches[0]
	if s.FrontIndex != 0 || s.VictimIndex != 1 || s.BackIndex != 2 {
		t.Fatalf("wrong indices: %+v", s)
	}
	if s.Attacker != "eve" || s.Victim != "alice" {
		t.Fatalf("wrong actors: %+v", s)
	}
	if s.VictimLoss != wantLoss.String() {
		t.Fatalf("victim loss = %s, want %s", s.VictimLoss, wantLoss)
	}
	if s.AttackerProfit != profit.String() {
		t.Fatalf("attacker profit = %s, want %s", s.AttackerProfit, profit)
	}
}

// TestDetectSandwichesIgnoresNormalFlow: a user's unprofitable buy-then-sell
// round trip is ordinary flow, not an attack, and must not be flagged.
func TestDetectSandwichesIgnoresNormalFlow(t *testing.T) {
	p := NewPool(bi(1_000_000), bi(1_000_000), 997, 1000)
	trades := []Trade{
		{Actor: "a", XToY: true, AmountIn: bi(1_000)},
		{Actor: "b", XToY: false, AmountIn: bi(1_000)},
		{Actor: "c", XToY: true, AmountIn: bi(1_000)},
		{Actor: "a", XToY: false, AmountIn: bi(900)},
	}
	if got := DetectSandwiches(p, trades, 10); len(got) != 0 {
		t.Fatalf("expected no sandwiches, got %+v", got)
	}
}

// TestExecuteSequenceRejection: a trade whose protection is violated mid-
// sequence is rejected, logged, and leaves the pool unchanged.
func TestExecuteSequenceRejection(t *testing.T) {
	p := NewPool(bi(1_000_000), bi(1_000_000), 997, 1000)

	// Alice quotes 30k X->Y now and sets a 0.5% tolerance...
	quote, err := p.Quote(true, bi(30_000))
	if err != nil {
		t.Fatal(err)
	}
	minOut := MinOutFromBps(quote, 50)

	// ...but a whale moves the price before her trade executes.
	trades := []Trade{
		{Actor: "whale", XToY: true, AmountIn: bi(200_000)},
		{Actor: "alice", XToY: true, AmountIn: bi(30_000), MinAmountOut: minOut},
	}
	logs := ExecuteSequence(p, trades, 1.0)
	if len(logs) != 2 {
		t.Fatalf("expected 2 log entries, got %d", len(logs))
	}
	if !logs[0].Executed {
		t.Fatal("whale trade should execute")
	}
	if logs[1].Executed {
		t.Fatal("alice trade should have been rejected by slippage protection")
	}
	if logs[1].RejectReason == "" {
		t.Fatal("rejection reason must be reported")
	}
	if logs[0].KAfter != logs[1].KAfter {
		t.Fatal("rejected trade must not change k")
	}
	if !logs[0].Anomaly {
		t.Fatal("whale trade should be flagged as a price anomaly at 1% threshold")
	}
}
