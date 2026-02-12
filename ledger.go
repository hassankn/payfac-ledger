package ledger

import (
	"errors"
	"fmt"
	"time"
)

// PayoutFunc is called to issue a payout to a merchant's bank account.
// Returns an error if the payout fails.
type PayoutFunc func(merchantID string, amount int64, reference string) error

// Ledger is the core ledger that tracks funds through double-entry bookkeeping.
type Ledger struct {
	transactions     map[string]*Transaction // keyed by transaction_id
	refIndex         map[string]string       // processor_ref_id -> transaction_id
	entries          []LedgerEntry
	nextEntryID      int
	processedFiles   map[string]bool   // settlement file IDs already processed
	settlementTotals map[string]int64  // settlement_date -> expected total
	settlementDates  map[string]string // settlement_date -> file tracking
	payoutFunc       PayoutFunc
}

// NewLedger creates a new in-memory ledger.
func NewLedger(payoutFunc PayoutFunc) *Ledger {
	return &Ledger{
		transactions:     make(map[string]*Transaction),
		refIndex:         make(map[string]string),
		entries:          make([]LedgerEntry, 0),
		processedFiles:   make(map[string]bool),
		settlementTotals: make(map[string]int64),
		settlementDates:  make(map[string]string),
		payoutFunc:       payoutFunc,
	}
}

// RecordAuthorization records an approved card authorization in the ledger.
// It creates a journal entry: debit CardProcessor, credit Pending.
func (l *Ledger) RecordAuthorization(txn Transaction) error {
	if txn.TransactionID == "" {
		return errors.New("transaction_id is required")
	}
	if txn.MerchantID == "" {
		return errors.New("merchant_id is required")
	}
	if txn.Amount <= 0 {
		return errors.New("amount must be positive")
	}
	if txn.ProcessorRefID == "" {
		return errors.New("processor_reference_id is required")
	}

	// Idempotency: skip if already recorded.
	if _, exists := l.transactions[txn.TransactionID]; exists {
		return fmt.Errorf("transaction %s already exists", txn.TransactionID)
	}

	txn.Status = StatusPending
	txn.CreatedAt = time.Now()
	l.transactions[txn.TransactionID] = &txn
	l.refIndex[txn.ProcessorRefID] = txn.TransactionID

	l.addEntry(txn.TransactionID, txn.MerchantID, AccountCardProcessor, AccountPending, txn.Amount, "authorization")

	return nil
}

// ProcessSettlementFile processes a daily settlement file from the card processor.
// It moves matched transactions from Pending to Settling and flags unknown rows.
func (l *Ledger) ProcessSettlementFile(file SettlementFile) (*SettlementFileResult, error) {
	if file.FileID == "" {
		return nil, errors.New("file_id is required")
	}

	// Idempotency: skip if already processed.
	if l.processedFiles[file.FileID] {
		return &SettlementFileResult{}, nil
	}

	result := &SettlementFileResult{}
	var totalAmount int64

	for _, row := range file.Rows {
		totalAmount += row.Amount

		txnID, found := l.refIndex[row.ProcessorRefID]
		if !found {
			result.Unmatched++
			result.UnmatchedRows = append(result.UnmatchedRows, row)
			continue
		}

		txn := l.transactions[txnID]

		// Skip if already settled (shouldn't create duplicate entries).
		if txn.Status != StatusPending {
			result.AlreadySettled++
			continue
		}

		txn.Status = StatusSettling
		txn.SettlementDate = file.Date
		l.addEntry(txn.TransactionID, txn.MerchantID, AccountPending, AccountSettling, row.Amount, "settlement")
		result.Matched++
	}

	l.processedFiles[file.FileID] = true
	l.settlementTotals[file.Date] = totalAmount

	return result, nil
}

// ReconcileBankDeposit confirms a bank deposit matches the expected settlement total
// and moves all settling transactions for that date to Available.
func (l *Ledger) ReconcileBankDeposit(amount int64, settlementDate string) error {
	expected, exists := l.settlementTotals[settlementDate]
	if !exists {
		return fmt.Errorf("no settlement found for date %s", settlementDate)
	}

	if amount != expected {
		return fmt.Errorf("deposit mismatch for %s: expected %d, got %d", settlementDate, expected, amount)
	}

	// Move all settling transactions for this date to available.
	for _, txn := range l.transactions {
		if txn.Status == StatusSettling && txn.SettlementDate == settlementDate {
			txn.Status = StatusAvailable
			l.addEntry(txn.TransactionID, txn.MerchantID, AccountSettling, AccountAvailable, txn.Amount, "bank_reconciliation")
		}
	}

	return nil
}

// ExecutePayoutBatch calculates available balances per merchant, issues payouts,
// and moves successful payouts to Funded.
func (l *Ledger) ExecutePayoutBatch() []PayoutResult {
	// Aggregate available balances per merchant.
	available := make(map[string]int64)
	for _, txn := range l.transactions {
		if txn.Status == StatusAvailable {
			available[txn.MerchantID] += txn.Amount
		}
	}

	var results []PayoutResult

	for merchantID, amount := range available {
		if amount <= 0 {
			continue
		}

		reference := fmt.Sprintf("payout-%s-%d", merchantID, time.Now().UnixNano())
		err := l.payoutFunc(merchantID, amount, reference)
		result := PayoutResult{
			MerchantID: merchantID,
			Amount:     amount,
			Success:    err == nil,
			Error:      err,
		}
		results = append(results, result)

		if err == nil {
			// Move all available transactions for this merchant to posted.
			for _, txn := range l.transactions {
				if txn.MerchantID == merchantID && txn.Status == StatusAvailable {
					txn.Status = StatusFunded
					l.addEntry(txn.TransactionID, txn.MerchantID, AccountAvailable, AccountFunded, txn.Amount, "payout")
				}
			}
		}
	}

	return results
}

// GetMerchantBalance returns the current balance in each state for a merchant.
// Balances are computed from individual debit/credit entries per account.
func (l *Ledger) GetMerchantBalance(merchantID string) MerchantBalance {
	bal := MerchantBalance{MerchantID: merchantID}

	for _, e := range l.entries {
		if e.MerchantID != merchantID {
			continue
		}

		switch e.EntryType {
		case Credit:
			addToAccount(&bal, e.Account, e.Amount)
		case Debit:
			addToAccount(&bal, e.Account, -e.Amount)
		}
	}

	return bal
}

// addToAccount adds an amount to the appropriate field in MerchantBalance.
func addToAccount(bal *MerchantBalance, acct Account, amount int64) {
	switch acct {
	case AccountPending:
		bal.Pending += amount
	case AccountSettling:
		bal.Settling += amount
	case AccountAvailable:
		bal.Available += amount
	case AccountFunded:
		bal.Funded += amount
	}
}

// addEntry creates a proper double-entry journal entry: two rows (debit + credit)
// linked by the same journal ID.
func (l *Ledger) addEntry(txnID, merchantID string, debitAcct, creditAcct Account, amount int64, ref string) {
	l.nextEntryID++
	journalID := l.nextEntryID
	now := time.Now()

	// Debit entry (money leaving this account).
	l.entries = append(l.entries, LedgerEntry{
		ID:            journalID*2 - 1,
		JournalID:     journalID,
		TransactionID: txnID,
		MerchantID:    merchantID,
		Account:       debitAcct,
		EntryType:     Debit,
		Amount:        amount,
		CreatedAt:     now,
		Reference:     ref,
	})

	// Credit entry (money entering this account).
	l.entries = append(l.entries, LedgerEntry{
		ID:            journalID * 2,
		JournalID:     journalID,
		TransactionID: txnID,
		MerchantID:    merchantID,
		Account:       creditAcct,
		EntryType:     Credit,
		Amount:        amount,
		CreatedAt:     now,
		Reference:     ref,
	})
}
