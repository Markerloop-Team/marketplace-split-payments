package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/markerloop/marketplace-split-payments/internal/api"
	"github.com/markerloop/marketplace-split-payments/internal/checkout"
	"github.com/markerloop/marketplace-split-payments/internal/money"
	"github.com/markerloop/marketplace-split-payments/internal/order"
	"github.com/markerloop/marketplace-split-payments/internal/outbox"
	"github.com/markerloop/marketplace-split-payments/internal/outbox/kafka"
	"github.com/markerloop/marketplace-split-payments/internal/payment/fake"
	"github.com/markerloop/marketplace-split-payments/internal/storage/postgres"
)

type config struct {
	addr          string
	databaseURL   string
	kafkaBrokers  []string
	webhookSecret string
	commission    money.BPS
	simulator     bool
}

func loadConfig() (config, error) {
	c := config{
		addr:          env("HTTP_ADDR", ":8080"),
		databaseURL:   os.Getenv("DATABASE_URL"),
		webhookSecret: os.Getenv("WEBHOOK_SECRET"),
		simulator:     os.Getenv("PAYMENT_SIMULATOR") == "true",
	}
	if b := os.Getenv("KAFKA_BROKERS"); b != "" {
		c.kafkaBrokers = strings.Split(b, ",")
	}
	bps, err := strconv.ParseInt(env("COMMISSION_BPS", "1000"), 10, 64)
	if err != nil || !money.BPS(bps).Valid() {
		return c, fmt.Errorf("COMMISSION_BPS must be 0..10000")
	}
	c.commission = money.BPS(bps)
	if c.databaseURL == "" || c.webhookSecret == "" {
		return c, errors.New("DATABASE_URL and WEBHOOK_SECRET are required")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool); err != nil {
		return err
	}
	store := postgres.New(pool)

	var pub outbox.Publisher = outbox.LogPublisher{Log: log}
	if len(cfg.kafkaBrokers) > 0 {
		kp, err := kafka.New(cfg.kafkaBrokers)
		if err != nil {
			return err
		}
		defer kp.Close()
		pub = kp
	}
	go outbox.NewRelay(store, pub, log).Run(ctx)

	provider := fake.New(cfg.webhookSecret)
	svc := checkout.New(store, provider, order.FlatRates{Default: cfg.commission})
	handler := api.New(svc, log)
	if cfg.simulator {
		handler.EnableSimulator(provider)
		log.Warn("payment simulator routes enabled")
	}

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
