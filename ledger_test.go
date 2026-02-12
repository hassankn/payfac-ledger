package ledger

import (
	"errors"
	"testing"
)

// TestHappyPath runs through the full lifecycle:
// authorize -> settle -> reconcile -> payout -> disburse to merchant bank -> verify all balances zero.
func TestHappyPath(t *testing.T) {
	payoutFunc := func(merchantID string, amount int64, reference string) error {
		return nil // always succeeds
	}

	l := NewLedger(payoutFunc)

	// 1. Authorize transactions.
	txns := []Transaction{
		{TransactionID: "txn-1", MerchantID: "merchant-A", CardNumber: "4242", Amount: 1000, ProcessorRefID: "ref-1"},
		{TransactionID: "txn-2", MerchantID: "merchant-A", CardNumber: "4242", Amount: 2000, ProcessorRefID: "ref-2"},
		{TransactionID: "txn-3", MerchantID: "merchant-B", CardNumber: "5555", Amount: 3000, ProcessorRefID: "ref-3"},
	}
	for _, txn := range txns {
		if err := l.RecordAuthorization(txn); err != nil {
			t.Fatalf("RecordAuthorization(%s): %v", txn.TransactionID, err)
		}
	}

	// Check pending balances.
	balA := l.GetMerchantBalance("merchant-A")
	if balA.Pending != 3000 {
		t.Errorf("merchant-A pending: got %d, want 3000", balA.Pending)
	}
	balB := l.GetMerchantBalance("merchant-B")
	if balB.Pending != 3000 {
		t.Errorf("merchant-B pending: got %d, want 3000", balB.Pending)
	}

	// 2. Process settlement file.
	result, err := l.ProcessSettlementFile(SettlementFile{
		FileID: "file-1",
		Date:   "2026-02-10",
		Rows: []SettlementRow{
			{ProcessorRefID: "ref-1", MerchantID: "merchant-A", Amount: 1000},
			{ProcessorRefID: "ref-2", MerchantID: "merchant-A", Amount: 2000},
			{ProcessorRefID: "ref-3", MerchantID: "merchant-B", Amount: 3000},
		},
	})
	if err != nil {
		t.Fatalf("ProcessSettlementFile: %v", err)
	}
	if result.Matched != 3 {
		t.Errorf("matched: got %d, want 3", result.Matched)
	}
	if len(result.UnmatchedRows) != 0 {
		t.Errorf("unmatched: got %d, want 0", len(result.UnmatchedRows))
	}

	// Check settling balances (pending should be zero now).
	balA = l.GetMerchantBalance("merchant-A")
	if balA.Pending != 0 {
		t.Errorf("merchant-A pending after settlement: got %d, want 0", balA.Pending)
	}
	if balA.Settling != 3000 {
		t.Errorf("merchant-A settling: got %d, want 3000", balA.Settling)
	}

	// 3. Reconcile bank deposit.
	err = l.ReconcileBankDeposit(BankDeposit{Amount: 6000, SettlementDate: "2026-02-10"}) // 1000+2000+3000
	if err != nil {
		t.Fatalf("ReconcileBankDeposit: %v", err)
	}

	balA = l.GetMerchantBalance("merchant-A")
	if balA.Settling != 0 {
		t.Errorf("merchant-A settling after reconcile: got %d, want 0", balA.Settling)
	}
	if balA.Available != 3000 {
		t.Errorf("merchant-A available: got %d, want 3000", balA.Available)
	}

	// 4. Execute payout batch.
	payouts := l.ExecutePayoutBatch()
	if len(payouts) != 2 {
		t.Fatalf("payouts: got %d, want 2", len(payouts))
	}
	for _, p := range payouts {
		if !p.Success {
			t.Errorf("payout for %s failed: %v", p.MerchantID, p.Error)
		}
	}

	// 5. All merchant balances should be zero (money fully disbursed).
	balA = l.GetMerchantBalance("merchant-A")
	if balA.Pending != 0 || balA.Settling != 0 || balA.Available != 0 || balA.Funded != 0 {
		t.Errorf("merchant-A should have zero in all states: %+v", balA)
	}
	balB = l.GetMerchantBalance("merchant-B")
	if balB.Pending != 0 || balB.Settling != 0 || balB.Available != 0 || balB.Funded != 0 {
		t.Errorf("merchant-B should have zero in all states: %+v", balB)
	}

	// 6. System-wide balance: all zero (money has exited to merchant banks).
	sys := l.GetSystemBalance()
	if sys.Pending != 0 || sys.Settling != 0 || sys.Available != 0 || sys.Funded != 0 {
		t.Errorf("system should have zero in all states: %+v", sys)
	}
}

// TestUnknownSettlementRow verifies that settlement rows without a matching
// transaction are flagged but don't block processing of valid rows.
func TestUnknownSettlementRow(t *testing.T) {
	l := NewLedger(nil)

	_ = l.RecordAuthorization(Transaction{
		TransactionID: "txn-1", MerchantID: "m1", CardNumber: "4242", Amount: 500, ProcessorRefID: "ref-1",
	})

	result, err := l.ProcessSettlementFile(SettlementFile{
		FileID: "file-1",
		Date:   "2026-02-10",
		Rows: []SettlementRow{
			{ProcessorRefID: "ref-1", MerchantID: "m1", Amount: 500},
			{ProcessorRefID: "ref-unknown", MerchantID: "m2", Amount: 999},
		},
	})
	if err != nil {
		t.Fatalf("ProcessSettlementFile: %v", err)
	}

	if result.Matched != 1 {
		t.Errorf("matched: got %d, want 1", result.Matched)
	}
	if len(result.UnmatchedRows) != 1 {
		t.Errorf("unmatched: got %d, want 1", len(result.UnmatchedRows))
	}
	if len(result.UnmatchedRows) != 1 || result.UnmatchedRows[0].ProcessorRefID != "ref-unknown" {
		t.Errorf("unmatched rows: got %+v", result.UnmatchedRows)
	}

	// Valid transaction should still have moved to settling.
	bal := l.GetMerchantBalance("m1")
	if bal.Settling != 500 {
		t.Errorf("m1 settling: got %d, want 500", bal.Settling)
	}
}

// TestDepositMismatch verifies that a bank deposit that doesn't match the
// expected settlement total is rejected and no state changes occur.
func TestDepositMismatch(t *testing.T) {
	l := NewLedger(nil)

	_ = l.RecordAuthorization(Transaction{
		TransactionID: "txn-1", MerchantID: "m1", CardNumber: "4242", Amount: 1000, ProcessorRefID: "ref-1",
	})

	_, _ = l.ProcessSettlementFile(SettlementFile{
		FileID: "file-1",
		Date:   "2026-02-10",
		Rows:   []SettlementRow{{ProcessorRefID: "ref-1", MerchantID: "m1", Amount: 1000}},
	})

	// Wrong amount.
	err := l.ReconcileBankDeposit(BankDeposit{Amount: 999, SettlementDate: "2026-02-10"})
	if err == nil {
		t.Fatal("expected error for mismatched deposit, got nil")
	}

	// Transaction should still be in settling, not available.
	bal := l.GetMerchantBalance("m1")
	if bal.Settling != 1000 {
		t.Errorf("m1 settling: got %d, want 1000", bal.Settling)
	}
	if bal.Available != 0 {
		t.Errorf("m1 available: got %d, want 0", bal.Available)
	}
}

// TestIdempotentSettlement verifies that processing the same settlement file
// twice does not create duplicate entries.
func TestIdempotentSettlement(t *testing.T) {
	l := NewLedger(nil)

	_ = l.RecordAuthorization(Transaction{
		TransactionID: "txn-1", MerchantID: "m1", CardNumber: "4242", Amount: 500, ProcessorRefID: "ref-1",
	})

	file := SettlementFile{
		FileID: "file-1",
		Date:   "2026-02-10",
		Rows:   []SettlementRow{{ProcessorRefID: "ref-1", MerchantID: "m1", Amount: 500}},
	}

	// First processing.
	result1, _ := l.ProcessSettlementFile(file)
	if result1.Matched != 1 {
		t.Errorf("first pass matched: got %d, want 1", result1.Matched)
	}

	// Second processing — should be a no-op.
	result2, _ := l.ProcessSettlementFile(file)
	if result2.Matched != 0 && result2.AlreadySettled != 0 {
		t.Errorf("second pass should be no-op: %+v", result2)
	}

	// Balance should only reflect one settlement.
	bal := l.GetMerchantBalance("m1")
	if bal.Settling != 500 {
		t.Errorf("m1 settling: got %d, want 500", bal.Settling)
	}
}

// TestFailedPayoutRetry verifies that a failed payout leaves money in Available
// and a subsequent batch can successfully retry it.
func TestFailedPayoutRetry(t *testing.T) {
	failFirst := true
	payoutFunc := func(merchantID string, amount int64, reference string) error {
		if failFirst {
			failFirst = false
			return errors.New("bank unavailable")
		}
		return nil
	}

	l := NewLedger(payoutFunc)

	// Setup: authorize, settle, reconcile.
	_ = l.RecordAuthorization(Transaction{
		TransactionID: "txn-1", MerchantID: "m1", CardNumber: "4242", Amount: 1000, ProcessorRefID: "ref-1",
	})
	_, _ = l.ProcessSettlementFile(SettlementFile{
		FileID: "file-1",
		Date:   "2026-02-10",
		Rows:   []SettlementRow{{ProcessorRefID: "ref-1", MerchantID: "m1", Amount: 1000}},
	})
	_ = l.ReconcileBankDeposit(BankDeposit{Amount: 1000, SettlementDate: "2026-02-10"})

	// First batch — should fail.
	results := l.ExecutePayoutBatch()
	var m1Result *PayoutResult
	for i := range results {
		if results[i].MerchantID == "m1" {
			m1Result = &results[i]
		}
	}
	if m1Result == nil || m1Result.Success {
		t.Fatal("expected m1 payout to fail on first attempt")
	}

	// Money should still be available.
	bal := l.GetMerchantBalance("m1")
	if bal.Available != 1000 {
		t.Errorf("m1 available after failed payout: got %d, want 1000", bal.Available)
	}

	// Second batch — should succeed.
	results = l.ExecutePayoutBatch()
	for _, r := range results {
		if r.MerchantID == "m1" && !r.Success {
			t.Errorf("expected m1 payout to succeed on retry, got error: %v", r.Error)
		}
	}

	// Now balance should be zero.
	bal = l.GetMerchantBalance("m1")
	if bal.Available != 0 {
		t.Errorf("m1 available after retry: got %d, want 0", bal.Available)
	}
}
