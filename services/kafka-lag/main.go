// Command kafka-lag exports to Prometheus the lag of every consumer group and
// the end offset of every partition, read from Kafka at each scrape (ADR 0017).
// It measures from outside the consumers, so a consumer that is stuck or down
// still shows up as a growing lag.
//
// Configuration:
//
//	KAFKA_BROKERS  comma separated seed brokers (kafka:9092)
//	HTTP_ADDR      address of the HTTP server (:8087)
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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/httpmetrics"
)

// readTimeout stays below the 10 s scrape timeout of Prometheus.
const readTimeout = 5 * time.Second

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("kafka-lag stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	kafkaBrokers := strings.Split(getenv("KAFKA_BROKERS", "kafka:9092"), ",")
	httpAddr := getenv("HTTP_ADDR", ":8087")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	kafka, err := kgo.NewClient(kgo.SeedBrokers(kafkaBrokers...))
	if err != nil {
		return err
	}
	defer kafka.Close()
	prometheus.MustRegister(collector{src: kafkaSource{adm: kadm.NewClient(kafka)}, timeout: readTimeout})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := kafka.Ping(pingCtx); err != nil {
			http.Error(w, "kafka: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.Handle("GET /metrics", promhttp.Handler())

	srv := &http.Server{Addr: httpAddr, Handler: httpmetrics.Handler(mux), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("kafka-lag started", "kafka_brokers", kafkaBrokers, "http_addr", httpAddr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
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
