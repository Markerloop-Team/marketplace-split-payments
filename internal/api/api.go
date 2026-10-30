// Package api exposes the checkout service over HTTP.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/markerloop/marketplace-split-payments/internal/checkout"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/payment"
	"github.com/markerloop/marketplace-split-payments/internal/payment/fake"
	"github.com/markerloop/marketplace-split-payments/internal/payout"
)

const (
	maxBodyBytes    = 1 << 20
	maxWebhookBytes = 64 << 10
)

type Server struct {
	svc *checkout.Service
	log *slog.Logger
	mux *http.ServeMux
}

func New(svc *checkout.Service, log *slog.Logger) *Server {
	s := &Server{svc: svc, log: log, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	s.mux.HandleFunc("POST /v1/orders", s.placeOrder)
	s.mux.HandleFunc("GET /v1/orders/{id}", s.getOrder)
	s.mux.HandleFunc("POST /v1/orders/{id}/refunds", s.refund)
	s.mux.HandleFunc("POST /v1/webhooks/payments", s.webhook)
	s.mux.HandleFunc("POST /v1/payouts/run", s.runPayouts)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// EnableSimulator adds routes that make the fake provider deliver a webhook,
// standing in for the customer paying. Never mount these in production.
func (s *Server) EnableSimulator(p *fake.Provider) {
	deliver := func(settle func(string) (http.Header, []byte, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			h, body, err := settle(r.PathValue("id"))
			if err == nil {
				err = s.svc.HandleWebhook(r.Context(), h, body)
			}
			if err != nil {
				s.fail(w, r, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}
	s.mux.HandleFunc("POST /dev/payments/{id}/capture", deliver(p.Capture))
	s.mux.HandleFunc("POST /dev/payments/{id}/decline", deliver(p.Decline))
}

func (s *Server) placeOrder(w http.ResponseWriter, r *http.Request) {
	var req placeOrderRequest
	if !s.decode(w, r, &req) {
		return
	}
	in := checkout.PlaceOrderInput{
		CustomerID: req.CustomerID,
		Currency:   req.Currency,
		Discount:   req.Discount,
		Method:     req.Method,
	}
	for _, l := range req.Lines {
		in.Lines = append(in.Lines, checkout.LineInput{SKU: l.SKU, SellerID: l.SellerID, Qty: l.Qty, UnitPrice: l.UnitPrice})
	}

	co, err := s.svc.PlaceOrder(r.Context(), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp := toOrderResponse(co.Order, co.SubOrders)
	resp.Payment = &paymentResponse{
		ID:          co.Payment.ID,
		Method:      co.Payment.Method,
		RedirectURL: co.Payment.RedirectURL,
		QRPayload:   co.Payment.QRPayload,
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) getOrder(w http.ResponseWriter, r *http.Request) {
	o, subs, err := s.svc.GetOrder(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrderResponse(o, subs))
}

func (s *Server) refund(w http.ResponseWriter, r *http.Request) {
	var req refundRequest
	if !s.decode(w, r, &req) {
		return
	}
	in := checkout.RefundInput{
		OrderID:        r.PathValue("id"),
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	}
	for _, l := range req.Lines {
		in.Lines = append(in.Lines, order.ReturnLine{LineID: l.LineID, Qty: l.Qty})
	}

	ref, err := s.svc.Refund(r.Context(), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRefundResponse(ref))
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	}
	if err := s.svc.HandleWebhook(r.Context(), r.Header, body); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runPayouts(w http.ResponseWriter, r *http.Request) {
	var req runPayoutsRequest
	if !s.decode(w, r, &req) {
		return
	}
	if !req.Currency.Valid() || req.Minimum < 0 {
		writeError(w, http.StatusUnprocessableEntity, "currency and a non-negative minimum are required")
		return
	}
	ps, err := s.svc.RunPayouts(r.Context(), req.Currency, payout.Policy{Minimum: req.Minimum})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp := make([]payoutResponse, 0, len(ps))
	for _, p := range ps {
		resp = append(resp, payoutResponse{ID: p.ID, SellerID: p.SellerID, Amount: p.Amount, Currency: p.Currency, Status: p.Status})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, checkout.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, checkout.ErrNotFound), errors.Is(err, payment.ErrUnknownPayment):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, checkout.ErrNotRefundable), errors.Is(err, checkout.ErrPaymentMismatch),
		errors.Is(err, payment.ErrRefundRejected):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, payment.ErrInvalidSignature):
		writeError(w, http.StatusUnauthorized, "invalid signature")
	case errors.Is(err, payment.ErrMalformedEvent):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.log.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
