// Package ledger is a double-entry book of everything that moves money.
//
// Postings are signed: debits are positive, credits are negative. Every entry
// must sum to zero, so the book as a whole always sums to zero too.
package ledger

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
)

// Account is a ledger account code, "<type>:<name>".
type Account string

const (
	// Settlement is money held for us by the payment provider (asset).
	Settlement Account = "asset:psp_settlement"
	// CommissionRevenue is the platform's earned commission (revenue).
	CommissionRevenue Account = "revenue:commission"

	sellerPrefix = "liability:seller:"
)

// SellerPayable is what the platform owes a seller (liability).
func SellerPayable(id order.SellerID) Account { return Account(sellerPrefix + string(id)) }

// Seller extracts the seller from a payable account.
func (a Account) Seller() (order.SellerID, bool) {
	id, ok := strings.CutPrefix(string(a), sellerPrefix)
	return order.SellerID(id), ok
}

var (
	ErrUnbalanced    = errors.New("ledger: entry does not balance")
	ErrTooFewLegs    = errors.New("ledger: entry needs at least two postings")
	ErrZeroPosting   = errors.New("ledger: zero posting")
	ErrMissingFields = errors.New("ledger: entry is missing fields")
)

type Posting struct {
	Account Account
	Amount  money.Amount
}

func Debit(a Account, amt money.Amount) Posting  { return Posting{a, amt} }
func Credit(a Account, amt money.Amount) Posting { return Posting{a, -amt} }

// Entry is one balanced journal entry. Ref is unique across the book, which
// makes posting the same business event twice impossible.
type Entry struct {
	ID       string
	Ref      string
	Currency money.Currency
	Postings []Posting
}

func (e Entry) Validate() error {
	if e.ID == "" || e.Ref == "" || !e.Currency.Valid() {
		return ErrMissingFields
	}
	if len(e.Postings) < 2 {
		return ErrTooFewLegs
	}
	var sum money.Amount
	for _, p := range e.Postings {
		if p.Account == "" {
			return ErrMissingFields
		}
		if p.Amount == 0 {
			return fmt.Errorf("%w on %s", ErrZeroPosting, p.Account)
		}
		sum += p.Amount
	}
	if sum != 0 {
		return fmt.Errorf("%w: off by %d", ErrUnbalanced, sum)
	}
	return nil
}

// Balances are signed account balances in a single currency.
type Balances map[Account]money.Amount

func (b Balances) Apply(e Entry) {
	for _, p := range e.Postings {
		b[p.Account] += p.Amount
	}
}

// Total is the sum of all balances, which is zero for any valid book.
func (b Balances) Total() money.Amount {
	var s money.Amount
	for _, v := range b {
		s += v
	}
	return s
}

// Payables lists positive amounts owed to sellers, sorted by seller.
func (b Balances) Payables() []SellerBalance {
	var out []SellerBalance
	for acc, v := range b {
		if id, ok := acc.Seller(); ok && v < 0 {
			out = append(out, SellerBalance{SellerID: id, Amount: -v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SellerID < out[j].SellerID })
	return out
}

type SellerBalance struct {
	SellerID order.SellerID
	Amount   money.Amount
}

// postings drops zero legs so builders do not have to special-case a 0%
// commission or a fully discounted line.
func postings(ps ...Posting) []Posting {
	out := ps[:0]
	for _, p := range ps {
		if p.Amount != 0 {
			out = append(out, p)
		}
	}
	return out
}
