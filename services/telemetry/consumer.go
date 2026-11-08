package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/dlq"
)

const (
	maxBatchRows = 1000
	maxBatchWait = time.Second
	maxRetryWait = 30 * time.Second
)

var (
	consumed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "telemetry_records_consumed_total",
		Help: "Records consumed from telemetria.bruta, committed after they are handled.",
	})
	inserted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "telemetry_rows_inserted_total",
		Help: "Rows inserted into telemetry_raw.",
	})
	duplicates = promauto.NewCounter(prometheus.CounterOpts{
		Name: "telemetry_duplicates_dropped_total",
		Help: "Readings dropped because their seq was already seen for the same machine_id and boot_id.",
	})
	deadLettered = promauto.NewCounter(prometheus.CounterOpts{
		Name: "telemetry_dead_lettered_total",
		Help: "Records sent to telemetria.dlq because they break the contract.",
	})
	failures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "telemetry_failures_total",
		Help: "Failed attempts that are retried, by operation.",
	}, []string{"operation"})
	// The p95 of the load test (ADR 0017). It includes any difference between
	// the clock of the device, which sets ts, and the clock of this service.
	ingestDelay = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "telemetry_ingest_delay_seconds",
		Help:    "Time from the ts of a reading to its insert into telemetry_raw.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 300},
	})
)

type consumer struct {
	kafka *kgo.Client
	ch    driver.Conn
	seen  dedup
}

// run consumes telemetria.bruta until ctx is done, in batches of up to
// maxBatchRows rows or maxBatchWait, whichever comes first. The client blocks
// rebalances from each poll until AllowRebalance, so partitions only move
// between batches and offsets are never committed for a partition this
// instance no longer owns.
func (c *consumer) run(ctx context.Context) {
	for {
		var b batch
		for len(b.rows) < maxBatchRows && (b.records == 0 || time.Since(b.started) < maxBatchWait) {
			pollCtx, cancel := ctx, context.CancelFunc(func() {})
			if b.records > 0 {
				pollCtx, cancel = context.WithDeadline(ctx, b.started.Add(maxBatchWait))
			}
			fetches := c.kafka.PollRecords(pollCtx, maxBatchRows-len(b.rows))
			cancel()
			if ctx.Err() != nil {
				// Records not committed yet are consumed again after a restart.
				return
			}
			fetches.EachError(func(topic string, partition int32, err error) {
				if !errors.Is(err, context.DeadlineExceeded) {
					slog.Error("kafka fetch failed", "topic", topic, "partition", partition, "error", err)
				}
			})
			fetches.EachRecord(func(r *kgo.Record) { b.add(r, c.seen) })
		}
		if err := c.flush(ctx, &b); err != nil {
			return
		}
		c.kafka.AllowRebalance()
	}
}

// flush stores a batch and then commits its offsets. Every row already passed
// the contract, so a failure here comes from Kafka or ClickHouse, not from the
// data: it is retried until it works and nothing is committed meanwhile
// (ADR 0004). It returns an error only when ctx is done.
func (c *consumer) flush(ctx context.Context, b *batch) error {
	if len(b.dead) > 0 {
		err := retry(ctx, "dlq_produce", func(ctx context.Context) error {
			return c.kafka.ProduceSync(ctx, b.dead...).FirstErr()
		})
		if err != nil {
			return err
		}
		for _, r := range b.dead {
			slog.Warn("message sent to DLQ", "key", string(r.Key), "error", dlq.Header(r, dlq.HeaderError))
		}
	}
	if len(b.rows) > 0 {
		err := retry(ctx, "clickhouse_insert", func(ctx context.Context) error {
			return insertRows(ctx, c.ch, b.rows)
		})
		if err != nil {
			return err
		}
		now := time.Now()
		for _, row := range b.rows {
			ingestDelay.Observe(now.Sub(row.TS).Seconds())
		}
	}
	// A failed commit only means these records are consumed again later,
	// which deduplication makes harmless, so it is not retried.
	if err := c.kafka.CommitUncommittedOffsets(ctx); err != nil {
		failures.WithLabelValues("commit").Inc()
		slog.Warn("kafka commit failed, records will be consumed again", "error", err)
	}
	consumed.Add(float64(b.records))
	inserted.Add(float64(len(b.rows)))
	duplicates.Add(float64(b.duplicates))
	deadLettered.Add(float64(len(b.dead)))
	return nil
}

// retry calls fn until it succeeds or ctx is done, doubling the wait after each
// failure up to maxRetryWait. Each attempt has its own timeout.
func retry(ctx context.Context, operation string, fn func(context.Context) error) error {
	wait := time.Second
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := fn(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
		failures.WithLabelValues(operation).Inc()
		slog.Error("operation failed, retrying", "operation", operation, "error", err, "retry_in", wait.String())
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
		wait = min(2*wait, maxRetryWait)
	}
}
