package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/dlq"
)

const (
	maxPollRecords = 500
	maxAttempts    = 3 // attempts for an alert PostgreSQL refuses, before the DLQ (ADR 0004)
	attemptTimeout = 30 * time.Second
	maxRetryWait   = 30 * time.Second
)

var (
	consumed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "maintenance_records_consumed_total",
		Help: "Records consumed from alertas, committed after they are handled.",
	})
	alertsStored = promauto.NewCounter(prometheus.CounterOpts{
		Name: "maintenance_alerts_stored_total",
		Help: "Alerts stored in PostgreSQL, each linked to a work order.",
	})
	ordersOpened = promauto.NewCounter(prometheus.CounterOpts{
		Name: "maintenance_work_orders_opened_total",
		Help: "Work orders opened by an alert with no active order for its machine and type.",
	})
	duplicates = promauto.NewCounter(prometheus.CounterOpts{
		Name: "maintenance_duplicates_dropped_total",
		Help: "Alerts dropped because their alert_id was already stored.",
	})
	deadLettered = promauto.NewCounter(prometheus.CounterOpts{
		Name: "maintenance_dead_lettered_total",
		Help: "Records sent to telemetria.dlq because they break the contract or PostgreSQL refused them.",
	})
	failures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "maintenance_failures_total",
		Help: "Failed attempts, by operation.",
	}, []string{"operation"})
	// The ts of an alert is the ts of the reading that fired it, so this is the
	// delay from the reading to its work order (ADR 0017).
	alertDelay = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "maintenance_alert_delay_seconds",
		Help:    "Time from the ts of an alert to storing it in PostgreSQL with its work order.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300},
	})
)

// The series of each operation exist at 0 from the start. A series that first
// appears at 1 hides that failure from rate() and increase() (ADR 0018).
func init() {
	for _, op := range []string{"store_alert", "dlq_produce", "commit"} {
		failures.WithLabelValues(op)
	}
}

// alertStore is what the consumer needs from pgStore, so tests can replace it.
type alertStore interface {
	StoreAlert(ctx context.Context, a contract.Alert) (stored, error)
}

type consumer struct {
	kafka     *kgo.Client
	store     alertStore
	firstWait time.Duration // wait after the first failure, doubling up to maxRetryWait
}

// batch is what one poll of alertas turned into, counted after the commit.
type batch struct {
	records    int
	dead       []*kgo.Record
	opened     int
	joined     int
	duplicates int
}

// run consumes alertas until ctx is done. Each poll is handled alert by alert,
// then its dead letters are written and its offsets committed. The client blocks
// rebalances from each poll until AllowRebalance, so partitions only move
// between polls and offsets are never committed for a partition this instance
// no longer owns.
func (c *consumer) run(ctx context.Context) {
	for {
		fetches := c.kafka.PollRecords(ctx, maxPollRecords)
		if ctx.Err() != nil {
			// Records not committed yet are consumed again after a restart.
			return
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			slog.Error("kafka fetch failed", "topic", topic, "partition", partition, "error", err)
		})
		var b batch
		for _, r := range fetches.Records() {
			if err := c.handle(ctx, r, &b); err != nil {
				return
			}
		}
		if err := c.flush(ctx, &b); err != nil {
			return
		}
		c.kafka.AllowRebalance()
	}
}

// handle stores one alert record, failing as ADR 0004 says: a record that
// breaks the contract goes straight to the DLQ, an alert PostgreSQL refuses goes
// there after maxAttempts, and an unavailable database is retried until it comes
// back, holding the partition. It returns an error only when ctx is done.
func (c *consumer) handle(ctx context.Context, r *kgo.Record, b *batch) error {
	b.records++
	a, err := contract.ParseAlert(r.Value)
	if err != nil {
		b.dead = append(b.dead, dlq.Record(serviceName, "kafka:"+r.Topic, r.Key, r.Value, err))
		return nil
	}

	wait, refused := c.firstWait, 0
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		res, err := c.store.StoreAlert(attemptCtx, a)
		cancel()
		if err == nil {
			b.count(a, res)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		failures.WithLabelValues("store_alert").Inc()
		if reason := rejection(err); reason != nil {
			refused++
			if refused == maxAttempts {
				b.dead = append(b.dead, dlq.Record(serviceName, "kafka:"+r.Topic, r.Key, r.Value, reason))
				return nil
			}
			slog.Warn("alert refused by postgres, retrying", "alert_id", a.AlertID, "error", reason,
				"attempt", refused, "retry_in", wait.String())
		} else {
			slog.Error("postgres unavailable, retrying", "alert_id", a.AlertID, "error", err, "retry_in", wait.String())
		}
		if err := pause(ctx, wait); err != nil {
			return err
		}
		wait = min(2*wait, maxRetryWait)
	}
}

func (b *batch) count(a contract.Alert, res stored) {
	if res.Duplicate {
		b.duplicates++
		slog.Info("duplicate alert dropped", "alert_id", a.AlertID, "machine_id", a.MachineID)
		return
	}
	alertDelay.Observe(time.Since(a.TS).Seconds())
	if res.Opened {
		b.opened++
	} else {
		b.joined++
	}
	slog.Info("alert stored", "alert_id", a.AlertID, "machine_id", a.MachineID, "type", a.Type,
		"severity", a.Severity, "work_order_id", res.WorkOrderID, "opened", res.Opened)
}

// flush writes the dead letters of a batch and then commits its offsets. A
// failure to write them comes from Kafka, so it is retried until it works and
// nothing is committed meanwhile. It returns an error only when ctx is done.
func (c *consumer) flush(ctx context.Context, b *batch) error {
	if b.records == 0 {
		return nil
	}
	for wait := c.firstWait; len(b.dead) > 0; wait = min(2*wait, maxRetryWait) {
		produceCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		err := c.kafka.ProduceSync(produceCtx, b.dead...).FirstErr()
		cancel()
		if err == nil {
			break
		}
		failures.WithLabelValues("dlq_produce").Inc()
		slog.Error("kafka produce failed, retrying", "error", err, "retry_in", wait.String())
		if err := pause(ctx, wait); err != nil {
			return err
		}
	}
	for _, r := range b.dead {
		slog.Warn("message sent to DLQ", "key", string(r.Key), "error", dlq.Header(r, dlq.HeaderError))
	}
	// A failed commit only means these records are consumed again later, which
	// the alert_id makes harmless, so it is not retried.
	if err := c.kafka.CommitUncommittedOffsets(ctx); err != nil {
		failures.WithLabelValues("commit").Inc()
		slog.Warn("kafka commit failed, records will be consumed again", "error", err)
	}
	consumed.Add(float64(b.records))
	alertsStored.Add(float64(b.opened + b.joined))
	ordersOpened.Add(float64(b.opened))
	duplicates.Add(float64(b.duplicates))
	deadLettered.Add(float64(len(b.dead)))
	return nil
}

// pause waits for d, or returns ctx.Err() if ctx is done first.
func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
