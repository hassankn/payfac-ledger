package ledger

import "time"

// Account represents a named account in the double-entry ledger.
type Account string

// Date represents a calendar date in YYYY-MM-DD format.
type Date string

const (
	AccountCardProcessor Account = "card_processor" // External: funds at processor
	AccountPending       Account = "pending"        // Authorized, not yet settled
	AccountSettling      Account = "settling"       // On settlement file, not yet reconciled
	AccountAvailable     Account = "available"      // Reconciled with bank deposit
	AccountFunded        Account = "funded"         // Paid out to merchant
	AccountMerchantBank  Account = "merchant_bank"  // External: funds in merchants' bank accounts
)

// TransactionStatus tracks where a transaction is in its lifecycle.
type TransactionStatus string

const (
	StatusPending   TransactionStatus = "pending"
	StatusSettling  TransactionStatus = "settling"
	StatusAvailable TransactionStatus = "available"
	StatusFunded    TransactionStatus = "funded"
)

// Transaction represents a card payment submitted by a merchant.
type Transaction struct {
	TransactionID  string // internal unique ID for this transaction
	MerchantID     string // merchant who submitted the transaction
	CardNumber     string // last 4 + token
	Amount         int64  // in cents
	ProcessorRefID string // ID assigned by card processor, used to match settlement rows
	Status         TransactionStatus
	CreatedAt      time.Time
	SettlementDate Date // set when settled
}

// EntryType distinguishes debit from credit entries.
type EntryType string

const (
	Debit  EntryType = "debit"
	Credit EntryType = "credit"
)

// LedgerEntry is one row in the double-entry ledger.
// Every fund movement creates exactly two entries (one debit, one credit)
// linked by the same JournalID.
type LedgerEntry struct {
	ID            int
	JournalID     int // links the debit and credit halves
	TransactionID string
	MerchantID    string
	Account       Account
	EntryType     EntryType
	Amount        int64 // always positive
	CreatedAt     time.Time
	Reference     string // human-readable description
}

// SettlementRow is a single row from the processor's daily settlement file.
type SettlementRow struct {
	ProcessorRefID string
	MerchantID     string
	Amount         int64
}

// SettlementFile represents a daily settlement file from the card processor.
type SettlementFile struct {
	FileID string
	Date   Date
	Rows   []SettlementRow
}

// BankDeposit represents a deposit received into the PayFac's bank account.
type BankDeposit struct {
	Amount         int64
	SettlementDate Date
}

// SettlementFileResult summarizes what happened when processing a settlement file.
type SettlementFileResult struct {
	Matched        int
	AlreadySettled int
	UnmatchedRows  []SettlementRow
}

// Balance shows how much money is in each account state.
type Balance struct {
	MerchantID string // empty for system-wide balance
	Pending    int64
	Settling   int64
	Available  int64
	Funded     int64
}

// PayoutResult reports the outcome of a single merchant payout.
type PayoutResult struct {
	MerchantID string
	Amount     int64
	Success    bool
	Error      error
}
