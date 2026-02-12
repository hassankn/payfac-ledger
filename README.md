# PayFac Ledger

A simplified payment facilitation ledger system that tracks funds through their full lifecycle using double-entry bookkeeping.

## Project Structure

| File | Purpose |
|---|---|
| `models.go` | Domain types: accounts, entries, transactions, balances |
| `ledger.go` | All ledger logic: state transitions, balance queries |
| `ledger_test.go` | Happy path + 4 edge case tests |

## Design

### Double-Entry Bookkeeping

Every movement of funds creates **two separate ledger entries** — a debit (money leaving an account) and a credit (money entering an account) — linked by a shared `JournalID`. This allows independent verification: the sum of all debits must always equal the sum of all credits.

A single-row approach (one row with both debit and credit accounts) would be simpler, but locks us into 1:1 movements. Two separate rows allow a single journal entry to split funds across multiple accounts (e.g., partial settlements, fee deductions), which is important for extensibility.

All monetary amounts are stored as integer cents (`int64`) to avoid floating-point rounding errors.

**System accounts:**

| Account | Purpose |
|---|---|
| `card_processor` | External — funds held at the processor |
| `pending` | Authorized but not yet settled |
| `settling` | On settlement file, awaiting bank deposit |
| `available` | Reconciled with bank deposit, ready for payout |
| `funded` | Paid out to the merchant |

### State Transitions

A healthy transaction flows through: **Pending → Settling → Available → Funded**

Each transition moves money between accounts:

```
Authorization:    CardProcessor → Pending
Settlement:       Pending       → Settling
Reconciliation:   Settling      → Available
Payout:           Available     → Funded
```

A fully lifecycle'd transaction ends with zero balance in all intermediate states.

### Balance Queries

`GetMerchantBalance(merchantID)` computes balances by iterating ledger entries and summing credits minus debits for each account. A non-zero balance in any intermediate state indicates money stuck at that stage.

## Edge Cases

- **Unknown settlement rows** — Flagged in the result; other valid rows still process normally.
- **Deposit mismatch** — Reconciliation is rejected entirely if the bank deposit doesn't match the expected settlement total. No partial transitions.
- **Failed payouts** — Money stays in `available`; the merchant will be retried in the next batch.
- **Idempotency** — Settlement files are tracked by ID; reprocessing the same file is a no-op.

## Trade-offs

- **In-memory storage** — Simple and fast for this exercise, but not durable. A production system would use a relational database with transactions.
- **Sequential processing** — Settlement and payout processing iterate all transactions. Fine for small volumes; would need indexing at scale.
- **No concurrency** — The `Ledger` struct is not safe for concurrent use. A production system would need mutex locks or database-level isolation.

## What I'd Improve

- Add database persistence with proper transaction isolation
- Add per-merchant account indexing for faster balance queries
- Add event sourcing for a full audit trail
- Add concurrency safety (mutex or channel-based)
- Split payout logic into separate prepare/execute/confirm phases
- Add observability (logging, metrics, tracing)

## Running Tests

```bash
go test -v ./...
```
