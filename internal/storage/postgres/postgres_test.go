package postgres_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/markerloop/marketplace-split-payments/internal/checkout"
	"github.com/markerloop/marketplace-split-payments/internal/ledger"
	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/outbox"
	"github.com/markerloop/marketplace-split-payments/internal/payment"
	"github.com/markerloop/marketplace-split-payments/internal/payment/fake"
	"github.com/markerloop/marketplace-split-payments/internal/payout"
	"github.com/markerloop/marketplace-split-payments/internal/storage/postgres"
)

const templateDB = "split_template"

var (
	adminDSN   func(db string) string
	skipReason string
	dbCounter  atomic.Int64
)

// One container per package run. Migrations are applied once to a template
// database, and every test gets its own copy of it.
func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		skipReason = "integration test skipped in -short mode"
		os.Exit(m.Run())
	}
	ctr, err := startPostgres()
	if err != nil {
		skipReason = "postgres container unavailable: " + err.Error()
		os.Exit(m.Run())
	}
	code := m.Run()
	_ = testcontainers.TerminateContainer(ctr)
	os.Exit(code)
}

func startPostgres() (ctr *tcpostgres.PostgresContainer, err error) {
	defer func() {
		// testcontainers panics instead of returning an error when no Docker
		// host can be found.
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	ctx := context.Background()
	ctr, err = tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		// Data on tmpfs: faster, and nothing to clean up on the Docker host.
		testcontainers.CustomizeRequest(testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Tmpfs: map[string]string{"/var/lib/postgresql/data": "rw"},
			},
		}),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(time.Minute)),
	)
	if err != nil {
		return nil, err
	}
	base, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, err
	}
	adminDSN = func(db string) string { return replaceDB(base, db) }

	admin, err := pgxpool.New(ctx, adminDSN("postgres"))
	if err != nil {
		return nil, err
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+templateDB); err != nil {
		return nil, err
	}

	tmpl, err := pgxpool.New(ctx, adminDSN(templateDB))
	if err != nil {
		return nil, err
	}
	defer tmpl.Close()
	return ctr, postgres.Migrate(ctx, tmpl)
}

func replaceDB(dsn, db string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + db
	return u.String()
}

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if skipReason != "" {
		t.Skip(skipReason)
	}
	ctx := context.Background()
	name := fmt.Sprintf("test_%d", dbCounter.Add(1))

	admin, err := pgxpool.New(ctx, adminDSN("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateDB)); err != nil {
		t.Fatal(err)
	}

	pool, err := pgxpool.New(ctx, adminDSN(name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type env struct {
	pool     *pgxpool.Pool
	store    *postgres.Store
	svc      *checkout.Service
	provider *fake.Provider
}

func newEnv(t *testing.T) env {
	pool := newPool(t)
	store := postgres.New(pool)
	provider := fake.New("secret")
	svc := checkout.New(store, provider, order.FlatRates{Default: 1200})
	return env{pool: pool, store: store, svc: svc, provider: provider}
}

func (e env) paidOrder(t *testing.T) checkout.Checkout {
	t.Helper()
	ctx := context.Background()
	co, err := e.svc.PlaceOrder(ctx, checkout.PlaceOrderInput{
		CustomerID: "c1",
		Currency:   money.KZT,
		Discount:   777,
		Method:     payment.MethodCard,
		Lines: []checkout.LineInput{
			{SKU: "kettle", SellerID: "home", Qty: 1, UnitPrice: 18990},
			{SKU: "mug", SellerID: "home", Qty: 4, UnitPrice: 1490},
			{SKU: "tea", SellerID: "grocery", Qty: 3, UnitPrice: 2350},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h, body, err := e.provider.Capture(co.Payment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.HandleWebhook(ctx, h, body); err != nil {
		t.Fatal(err)
	}
	return co
}

func (e env) balances(t *testing.T) ledger.Balances {
	t.Helper()
	var b ledger.Balances
	err := e.store.InTx(context.Background(), func(ctx context.Context, tx checkout.Tx) error {
		var err error
		b, err = tx.Balances(ctx, money.KZT)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (e env) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOrderRoundTrip(t *testing.T) {
	e := newEnv(t)
	co := e.paidOrder(t)

	o, subs, err := e.svc.GetOrder(context.Background(), co.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != order.StatusPaid || o.Total() != co.Order.Total() || o.PaymentID != co.Payment.ID {
		t.Fatalf("order = %+v", o)
	}
	if len(o.Lines) != 3 || o.Lines[0].SKU != "kettle" || o.Lines[2].SKU != "tea" {
		t.Fatalf("lines lost cart order: %+v", o.Lines)
	}
	if len(subs) != len(co.SubOrders) {
		t.Fatalf("sub-orders = %d", len(subs))
	}
	for i := range subs {
		if subs[i].Net != co.SubOrders[i].Net || len(subs[i].Lines) != len(co.SubOrders[i].Lines) {
			t.Fatalf("sub-order %d = %+v, want %+v", i, subs[i], co.SubOrders[i])
		}
	}
	if _, _, err := e.svc.GetOrder(context.Background(), "missing"); !errors.Is(err, checkout.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestConcurrentDuplicateWebhooksBookOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	co, err := e.svc.PlaceOrder(ctx, checkout.PlaceOrderInput{
		CustomerID: "c1", Currency: money.KZT, Method: payment.MethodQR,
		Lines: []checkout.LineInput{{SKU: "x", SellerID: "s1", Qty: 1, UnitPrice: 5000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h, body, _ := e.provider.Capture(co.Payment.ID)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- e.svc.HandleWebhook(ctx, h, body)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	if n := e.count(t, "ledger_entries"); n != 1 {
		t.Fatalf("ledger entries = %d", n)
	}
	if n := e.count(t, "outbox"); n != 2 {
		t.Fatalf("outbox rows = %d", n)
	}
}

func TestRefundAndPayoutKeepBookAtZero(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	co := e.paidOrder(t)

	tea := co.SubOrders[1].Lines[0]
	for i := range 3 {
		_, err := e.svc.Refund(ctx, checkout.RefundInput{
			OrderID:        co.Order.ID,
			IdempotencyKey: fmt.Sprintf("rma-%d", i),
			Lines:          []order.ReturnLine{{LineID: tea.ID, Qty: 1}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Retried request: must not refund again.
	if _, err := e.svc.Refund(ctx, checkout.RefundInput{
		OrderID: co.Order.ID, IdempotencyKey: "rma-0", Lines: []order.ReturnLine{{LineID: tea.ID, Qty: 1}},
	}); err != nil {
		t.Fatal(err)
	}

	b := e.balances(t)
	if b[ledger.SellerPayable("grocery")] != 0 {
		t.Fatalf("grocery should be fully reversed: %v", b)
	}
	if b.Total() != 0 {
		t.Fatalf("book total = %d", b.Total())
	}

	ps, err := e.svc.RunPayouts(ctx, money.KZT, payout.Policy{Minimum: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].SellerID != "home" || ps[0].Amount != co.SubOrders[0].Net {
		t.Fatalf("payouts = %+v", ps)
	}
	if b := e.balances(t); b.Total() != 0 || b[ledger.SellerPayable("home")] != 0 {
		t.Fatalf("after payout: %v", b)
	}
}

func TestConcurrentPayoutRunsPayOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	co := e.paidOrder(t)

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		paid money.Amount
	)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ps, err := e.svc.RunPayouts(ctx, money.KZT, payout.Policy{Minimum: 1})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, p := range ps {
				paid += p.Amount
			}
		}()
	}
	wg.Wait()

	var net money.Amount
	for _, s := range co.SubOrders {
		net += s.Net
	}
	if paid != net {
		t.Fatalf("paid out %d, sellers were owed %d", paid, net)
	}
}

func TestDatabaseRejectsUnbalancedEntry(t *testing.T) {
	e := newEnv(t)
	err := e.store.InTx(context.Background(), func(ctx context.Context, tx checkout.Tx) error {
		return tx.PostEntry(ctx, ledger.Entry{
			ID: "bad", Ref: "bad", Currency: money.KZT,
			Postings: []ledger.Posting{ledger.Debit(ledger.Settlement, 100), ledger.Credit(ledger.CommissionRevenue, 99)},
		})
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("err = %v, want check_violation", err)
	}
	if n := e.count(t, "ledger_entries"); n != 0 {
		t.Fatalf("unbalanced entry persisted")
	}
}

func TestLedgerIsAppendOnly(t *testing.T) {
	e := newEnv(t)
	e.paidOrder(t)
	if _, err := e.pool.Exec(context.Background(), "UPDATE ledger_postings SET amount = amount + 1"); err == nil {
		t.Fatal("ledger update succeeded")
	}
	if _, err := e.pool.Exec(context.Background(), "DELETE FROM ledger_entries"); err == nil {
		t.Fatal("ledger delete succeeded")
	}
}

func TestOutboxRelay(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.paidOrder(t)
	e.paidOrder(t)

	pub := &outbox.MemoryPublisher{Err: errors.New("broker down")}
	relay := outbox.NewRelay(e.store, pub, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := relay.Drain(ctx); err == nil {
		t.Fatal("expected error")
	}

	pub.Err = nil
	n, err := relay.Drain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("published %d, want 6", n)
	}
	if n, _ := relay.Drain(ctx); n != 0 {
		t.Fatalf("republished %d", n)
	}
	msgs := pub.Messages()
	for i := 1; i < len(msgs); i++ {
		if msgs[i].ID <= msgs[i-1].ID {
			t.Fatal("messages out of order")
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	pool := newPool(t)
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
}
