// Command telemetry consumes telemetria.bruta with its own consumer group,
// drops repeated readings and writes the rest to ClickHouse in batches. Offsets
// are committed only after the insert, so delivery is at least once.
//
// It also serves the monitoring API: GET /machines and GET /machines/{id}/oee,
// with the machine registry from PostgreSQL and the readings from ClickHouse.
//
// Configuration:
//
//	KAFKA_BROKERS   comma separated seed brokers (kafka:9092)
//	CLICKHOUSE_DSN  ClickHouse native protocol address (clickhouse://industrial:industrial@clickhouse:9000/industrial)
//	POSTGRES_DSN    PostgreSQL address (postgres://industrial:industrial@postgres:5432/industrial?sslmode=disable)
//	HTTP_ADDR       address of the HTTP server (:8081)
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

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

const (
	serviceName   = "telemetry"
	consumerGroup = "telemetry"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("telemetry stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	kafkaBrokers := strings.Split(getenv("KAFKA_BROKERS", "kafka:9092"), ",")
	clickhouseDSN := getenv("CLICKHOUSE_DSN", "clickhouse://industrial:industrial@clickhouse:9000/industrial")
	postgresDSN := getenv("POSTGRES_DSN", "postgres://industrial:industrial@postgres:5432/industrial?sslmode=disable")
	httpAddr := getenv("HTTP_ADDR", ":8081")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	kafka, err := kgo.NewClient(
		kgo.SeedBrokers(kafkaBrokers...),
		kgo.ConsumerGroup(consumerGroup),
		kgo.ConsumeTopics(contract.TopicTelemetry),
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

	opts, err := clickhouse.ParseDSN(clickhouseDSN)
	if err != nil {
		return fmt.Errorf("CLICKHOUSE_DSN: %w", err)
	}
	ch, err := clickhouse.Open(opts)
	if err != nil {
		return err
	}
	defer ch.Close()

	// The pool connects on first use, so the consumer starts even with PostgreSQL down.
	pg, err := pgxpool.New(ctx, postgresDSN)
	if err != nil {
		return fmt.Errorf("POSTGRES_DSN: %w", err)
	}
	defer pg.Close()

	c := &consumer{kafka: kafka, ch: ch, seen: dedup{}}
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
		if err := ch.Ping(pingCtx); err != nil {
			http.Error(w, "clickhouse: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		if err := pg.Ping(pingCtx); err != nil {
			http.Error(w, "postgres: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	(&api{store: dbStore{pg: pg, ch: ch}, now: time.Now}).register(mux)

	srv := &http.Server{Addr: httpAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("telemetry started", "kafka_brokers", kafkaBrokers, "consumer_group", consumerGroup, "http_addr", httpAddr)
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
