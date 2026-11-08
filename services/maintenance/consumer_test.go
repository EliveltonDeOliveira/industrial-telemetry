package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/dlq"
)

var (
	notRegistered = &pgconn.PgError{
		Code:    "23503",
		Message: `insert or update on table "work_orders" violates foreign key constraint "work_orders_machine_id_fkey"`,
		Detail:  `Key (machine_id)=(maq-01) is not present in table "machines".`,
	}
	activeOrderExists = &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}
	unavailable       = errors.New("failed to connect to postgres: dial tcp: connect: connection refused")
)

// fakeStore fails with errs, one per call, and then returns res.
type fakeStore struct {
	errs  []error
	res   stored
	calls int
}

func (f *fakeStore) StoreAlert(_ context.Context, _ contract.Alert) (stored, error) {
	f.calls++
	if f.calls <= len(f.errs) {
		return stored{}, f.errs[f.calls-1]
	}
	return f.res, nil
}

func alertRecord(t *testing.T) *kgo.Record {
	value, err := os.ReadFile("../../contracts/examples/alert/valid/temperatura-alta.json")
	if err != nil {
		t.Fatal(err)
	}
	return &kgo.Record{Topic: contract.TopicAlerts, Key: []byte("maq-01"), Value: value}
}

func TestHandle(t *testing.T) {
	tests := []struct {
		name      string
		value     string // replaces the valid example when set
		errs      []error
		res       stored
		wantCalls int
		want      string // counts of the batch
		wantError string // error header of the dead letter, if any
	}{
		{name: "opens an order", res: stored{WorkOrderID: 7, Opened: true}, wantCalls: 1, want: "opened 1"},
		{name: "joins the active order", res: stored{WorkOrderID: 7}, wantCalls: 1, want: "joined 1"},
		{name: "alert_id already stored", res: stored{Duplicate: true}, wantCalls: 1, want: "duplicates 1"},
		{name: "breaks the contract", value: `{"schema_version": 1}`, wantCalls: 0,
			want: "dead 1", wantError: "alert_id: missing"},
		{name: "not JSON", value: `not json`, wantCalls: 0,
			want: "dead 1", wantError: "message is not a JSON object"},
		{name: "refused once, then stored", errs: []error{activeOrderExists}, res: stored{WorkOrderID: 7},
			wantCalls: 2, want: "joined 1"},
		{name: "refused every time", errs: []error{notRegistered, notRegistered, notRegistered},
			wantCalls: maxAttempts, want: "dead 1",
			wantError: `postgres: insert or update on table "work_orders" violates foreign key constraint ` +
				`"work_orders_machine_id_fkey": Key (machine_id)=(maq-01) is not present in table "machines". (SQLSTATE 23503)`},
		{name: "unavailable until it comes back",
			errs:      []error{unavailable, unavailable, unavailable, unavailable, unavailable},
			res:       stored{WorkOrderID: 7, Opened: true},
			wantCalls: 6, want: "opened 1"},
		{name: "only refusals count toward the DLQ",
			errs:      []error{notRegistered, unavailable, notRegistered, unavailable, notRegistered},
			wantCalls: 5, want: "dead 1", wantError: "SQLSTATE 23503"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := alertRecord(t)
			if tt.value != "" {
				r.Value = []byte(tt.value)
			}
			store := &fakeStore{errs: tt.errs, res: tt.res}
			c := &consumer{store: store, firstWait: time.Millisecond}
			var b batch
			if err := c.handle(context.Background(), r, &b); err != nil {
				t.Fatal(err)
			}

			if store.calls != tt.wantCalls {
				t.Errorf("StoreAlert called %d times, want %d", store.calls, tt.wantCalls)
			}
			if got := counts(b); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			if tt.wantError == "" {
				return
			}
			d := b.dead[0]
			if got := dlq.Header(d, dlq.HeaderError); !strings.Contains(got, tt.wantError) {
				t.Errorf("error header %q does not contain %q", got, tt.wantError)
			}
			if got := dlq.Header(d, dlq.HeaderSource); got != "kafka:alertas" {
				t.Errorf("source header %q, want kafka:alertas", got)
			}
			if got := dlq.Header(d, dlq.HeaderService); got != "maintenance" {
				t.Errorf("service header %q, want maintenance", got)
			}
			if string(d.Key) != string(r.Key) || string(d.Value) != string(r.Value) {
				t.Errorf("dead letter changed the record: key %q value %q", d.Key, d.Value)
			}
		})
	}
}

// counts lists the non-zero counts of a batch, such as "opened 1".
func counts(b batch) string {
	var parts []string
	for _, c := range []struct {
		name string
		n    int
	}{{"opened", b.opened}, {"joined", b.joined}, {"duplicates", b.duplicates}, {"dead", len(b.dead)}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", c.name, c.n))
		}
	}
	return strings.Join(parts, ", ")
}

func TestHandleStopsWhileUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	store := storeFunc(func(context.Context, contract.Alert) (stored, error) {
		if calls++; calls == 2 {
			cancel() // SIGTERM while the database is down
		}
		return stored{}, unavailable
	})
	c := &consumer{store: store, firstWait: time.Millisecond}
	var b batch
	if err := c.handle(ctx, alertRecord(t), &b); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if calls != 2 || len(b.dead) != 0 {
		t.Errorf("got %d calls and %d dead letters, want 2 calls and none", calls, len(b.dead))
	}
}

type storeFunc func(context.Context, contract.Alert) (stored, error)

func (f storeFunc) StoreAlert(ctx context.Context, a contract.Alert) (stored, error) {
	return f(ctx, a)
}

func TestRejection(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool // the alert is at fault
	}{
		{"foreign key violation", notRegistered, true},
		{"unique violation", activeOrderExists, true},
		{"wrapped", fmt.Errorf("insert alert: %w", notRegistered), true},
		{"invalid enum value", &pgconn.PgError{Code: "22P02"}, true},
		{"connection failure", unavailable, false},
		{"timeout", context.DeadlineExceeded, false},
		{"connection lost", &pgconn.PgError{Code: "08006"}, false},
		{"server shutting down", &pgconn.PgError{Code: "57P01"}, false},
		{"too many connections", &pgconn.PgError{Code: "53300"}, false},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, false},
		{"table missing, migrations not applied", &pgconn.PgError{Code: "42P01"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rejection(tt.err) != nil; got != tt.want {
				t.Errorf("rejection(%v) != nil is %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
