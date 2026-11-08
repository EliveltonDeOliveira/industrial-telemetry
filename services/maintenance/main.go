// Command maintenance consumes alertas and turns alerts into work orders in
// PostgreSQL: each alert opens an order, or joins the active order of the same
// machine and alert type (ADR 0013). Offsets are committed only after the
// alerts are stored, so delivery is at least once, and an alert_id already
// stored changes nothing.
//
// It also serves the work order API: GET /work-orders, GET /work-orders/{id}
// and PATCH /work-orders/{id}, which moves an order through its states (ADR 0014).
//
// Configuration:
//
//	KAFKA_BROKERS  comma separated seed brokers (kafka:9092)
//	POSTGRES_DSN   PostgreSQL address (postgres://industrial:industrial@postgres:5432/industrial?sslmode=disable)
//	HTTP_ADDR      address of the HTTP server (:8082)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

const (
	serviceName   = "maintenance"
	consumerGroup = "maintenance"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("maintenance stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	kafkaBrokers := strings.Split(getenv("KAFKA_BROKERS", "kafka:9092"), ",")
	postgresDSN := getenv("POSTGRES_DSN", "postgres://industrial:industrial@postgres:5432/industrial?sslmode=disable")
	httpAddr := getenv("HTTP_ADDR", ":8082")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	kafka, err := kgo.NewClient(
		kgo.SeedBrokers(kafkaBrokers...),
		kgo.ConsumerGroup(consumerGroup),
		kgo.ConsumeTopics(contract.TopicAlerts),
		// A new group reads alertas from the start, so alerts published before
		// maintenance first ran also become work orders (ADR 0013).
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
		// The same client produces to the DLQ. A missing topic is an
		// infrastructure problem: keep retrying (ADR 0004).
		kgo.UnknownTopicRetries(-1),
	)
	if err != nil {
		return err
	}
	defer kafka.Close()

	// The pool connects on first use, so the service starts even with PostgreSQL down.
	pg, err := pgxpool.New(ctx, postgresDSN)
	if err != nil {
		return fmt.Errorf("POSTGRES_DSN: %w", err)
	}
	defer pg.Close()

	c := &consumer{kafka: kafka, store: pgStore{pg: pg}, firstWait: time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.run(ctx)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := kafka.Ping(pingCtx); err != nil {
			http.Error(w, "kafka: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		if err := pg.Ping(pingCtx); err != nil {
			http.Error(w, "postgres: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	(&api{store: pgStore{pg: pg}}).register(mux)

	srv := &http.Server{Addr: httpAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("maintenance started", "kafka_brokers", kafkaBrokers, "consumer_group", consumerGroup, "http_addr", httpAddr)
	err = srv.ListenAndServe()
	stop()
	<-done
	// Leaving the group needs rebalances to be allowed.
	kafka.AllowRebalance()
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
