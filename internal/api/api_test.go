package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/markerloop/marketplace-split-payments/internal/api"
	"github.com/markerloop/marketplace-split-payments/internal/checkout"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/payment/fake"
	"github.com/markerloop/marketplace-split-payments/internal/storage/memory"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	provider := fake.New("secret")
	svc := checkout.New(memory.New(), provider, order.FlatRates{Default: 1000})
	srv := api.New(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.EnableSimulator(provider)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, ts *httptest.Server, method, path string, body any, header map[string]string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, ts.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestCheckoutOverHTTP(t *testing.T) {
	ts := newServer(t)

	status, created := do(t, ts, "POST", "/v1/orders", map[string]any{
		"customer_id": "c1",
		"currency":    "KZT",
		"method":      "qr",
		"lines": []map[string]any{
			{"sku": "a", "seller_id": "s1", "qty": 1, "unit_price": 10000},
			{"sku": "b", "seller_id": "s2", "qty": 2, "unit_price": 2500},
		},
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %v", status, created)
	}
	id := created["id"].(string)
	pay := created["payment"].(map[string]any)
	if pay["qr_payload"] == "" || created["total"].(float64) != 15000 {
		t.Fatalf("create response: %v", created)
	}

	if status, body := do(t, ts, "POST", "/dev/payments/"+pay["id"].(string)+"/capture", nil, nil); status != http.StatusNoContent {
		t.Fatalf("capture: %d %v", status, body)
	}
	_, got := do(t, ts, "GET", "/v1/orders/"+id, nil, nil)
	if got["status"] != string(order.StatusPaid) {
		t.Fatalf("status = %v", got["status"])
	}

	line := got["sub_orders"].([]any)[1].(map[string]any)["lines"].([]any)[0].(map[string]any)
	refund := map[string]any{"lines": []map[string]any{{"line_id": line["id"], "qty": 1}}}

	if status, _ := do(t, ts, "POST", "/v1/orders/"+id+"/refunds", refund, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("refund without idempotency key: %d", status)
	}
	status, r1 := do(t, ts, "POST", "/v1/orders/"+id+"/refunds", refund, map[string]string{"Idempotency-Key": "k1"})
	if status != http.StatusCreated || r1["amount"].(float64) != 2500 {
		t.Fatalf("refund: %d %v", status, r1)
	}
	_, r2 := do(t, ts, "POST", "/v1/orders/"+id+"/refunds", refund, map[string]string{"Idempotency-Key": "k1"})
	if r1["id"] != r2["id"] {
		t.Fatalf("retry returned a different refund: %v vs %v", r1["id"], r2["id"])
	}
}

func TestErrors(t *testing.T) {
	ts := newServer(t)
	tests := []struct {
		name   string
		method string
		path   string
		body   any
		want   int
	}{
		{"unknown order", "GET", "/v1/orders/nope", nil, http.StatusNotFound},
		{"empty cart", "POST", "/v1/orders", map[string]any{"currency": "KZT", "method": "card"}, http.StatusUnprocessableEntity},
		{"unknown field", "POST", "/v1/orders", map[string]any{"amount": 1.5}, http.StatusBadRequest},
		{"unsigned webhook", "POST", "/v1/webhooks/payments", map[string]any{"id": "evt"}, http.StatusUnauthorized},
		{"payout without currency", "POST", "/v1/payouts/run", map[string]any{}, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status, body := do(t, ts, tt.method, tt.path, tt.body, nil); status != tt.want {
				t.Fatalf("status = %d, want %d (%v)", status, tt.want, body)
			}
		})
	}
}
