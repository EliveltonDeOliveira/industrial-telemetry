package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

// The tests in this file need the PostgreSQL of the compose stack, with migrations applied:
//
//	POSTGRES_TEST_DSN=postgres://industrial:industrial@localhost:5432/industrial?sslmode=disable \
//	go test ./services/maintenance

// testStore connects to PostgreSQL and registers a machine of its own, whose
// alerts and work orders are deleted when the test ends.
func testStore(t *testing.T) (pgStore, string) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}
	ctx := context.Background()
	pg, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Cleanups run in reverse order, so the rows are deleted before the pool closes.
	t.Cleanup(pg.Close)
	machineID := fmt.Sprintf("test-%08x", rand.Uint32())
	_, err = pg.Exec(ctx, "INSERT INTO machines (machine_id, name, ideal_cycle_s) VALUES ($1, 'Test', 1)", machineID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, q := range []string{
			"DELETE FROM alerts WHERE machine_id = $1",
			"DELETE FROM work_order_events WHERE work_order_id IN (SELECT id FROM work_orders WHERE machine_id = $1)",
			"DELETE FROM work_orders WHERE machine_id = $1",
			"DELETE FROM machines WHERE machine_id = $1",
		} {
			if _, err := pg.Exec(ctx, q, machineID); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	return pgStore{pg: pg}, machineID
}

// newAlert returns a valid alert with a random alert_id.
func newAlert(machineID string, typ contract.AlertType) contract.Alert {
	return contract.Alert{
		SchemaVersion: contract.SchemaVersion,
		AlertID: fmt.Sprintf("%08x-%04x-4%03x-8%03x-%012x", rand.Uint32(), rand.Uint32N(1<<16),
			rand.Uint32N(1<<12), rand.Uint32N(1<<12), rand.Uint64N(1<<48)),
		MachineID: machineID,
		TS:        time.Date(2026, 11, 8, 14, 5, 0, 123_000_000, time.UTC),
		Type:      typ,
		Severity:  contract.SeverityHigh,
		Value:     78.2,
		Threshold: 65.0,
	}
}

func mustStore(t *testing.T, s pgStore, a contract.Alert) stored {
	t.Helper()
	res, err := s.StoreAlert(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// state describes the work orders of a machine, oldest first, with their
// events and alerts, such as "aberta [null>aberta] 2 alerts".
func state(t *testing.T, s pgStore, machineID string) string {
	t.Helper()
	rows, err := s.pg.Query(context.Background(), `SELECT o.status::text,
		(SELECT string_agg(coalesce(e.from_status::text, 'null') || '>' || e.to_status, ',' ORDER BY e.id)
		 FROM work_order_events e WHERE e.work_order_id = o.id),
		(SELECT count(*) FROM alerts a WHERE a.work_order_id = o.id)
		FROM work_orders o WHERE o.machine_id = $1 ORDER BY o.id`, machineID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var status, events string
		var alerts int64
		if err := rows.Scan(&status, &events, &alerts); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, fmt.Sprintf("%s [%s] %d alerts", status, events, alerts))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(parts, "; ")
}

func closeOrder(t *testing.T, s pgStore, id int64) {
	t.Helper()
	_, err := s.pg.Exec(context.Background(),
		"UPDATE work_orders SET status = 'concluida', closed_at = now() WHERE id = $1", id)
	if err != nil {
		t.Fatal(err)
	}
}

func TestStoreAlertOpensOrder(t *testing.T) {
	s, machineID := testStore(t)
	a := newAlert(machineID, contract.AlertHighTemperature)
	res := mustStore(t, s, a)
	if !res.Opened || res.Duplicate || res.WorkOrderID == 0 {
		t.Fatalf("got %+v, want a new order", res)
	}
	if got, want := state(t, s, machineID), "aberta [null>aberta] 1 alerts"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	var got contract.Alert
	var workOrderID int64
	err := s.pg.QueryRow(context.Background(), `SELECT alert_id::text, machine_id, ts, alert_type::text,
		severity::text, value, threshold, work_order_id FROM alerts WHERE alert_id = $1`, a.AlertID).
		Scan(&got.AlertID, &got.MachineID, &got.TS, &got.Type, &got.Severity, &got.Value, &got.Threshold, &workOrderID)
	if err != nil {
		t.Fatal(err)
	}
	got.SchemaVersion = a.SchemaVersion
	got.TS = got.TS.UTC()
	if got != a || workOrderID != res.WorkOrderID {
		t.Errorf("stored %+v in order %d, want %+v in order %d", got, workOrderID, a, res.WorkOrderID)
	}
}

func TestStoreAlertJoinsActiveOrder(t *testing.T) {
	s, machineID := testStore(t)
	first := mustStore(t, s, newAlert(machineID, contract.AlertLongStop))
	// An order in progress is still active.
	_, err := s.pg.Exec(context.Background(), "UPDATE work_orders SET status = 'em_andamento' WHERE id = $1", first.WorkOrderID)
	if err != nil {
		t.Fatal(err)
	}
	again := mustStore(t, s, newAlert(machineID, contract.AlertLongStop))
	if again.Opened || again.WorkOrderID != first.WorkOrderID {
		t.Errorf("got %+v, want order %d joined", again, first.WorkOrderID)
	}
	// Another type opens an order of its own.
	other := mustStore(t, s, newAlert(machineID, contract.AlertHighVibration))
	if !other.Opened {
		t.Errorf("got %+v, want a new order", other)
	}
	want := "em_andamento [null>aberta] 2 alerts; aberta [null>aberta] 1 alerts"
	if got := state(t, s, machineID); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStoreAlertDuplicate(t *testing.T) {
	s, machineID := testStore(t)
	a := newAlert(machineID, contract.AlertHighTemperature)
	first := mustStore(t, s, a)
	if res := mustStore(t, s, a); !res.Duplicate {
		t.Errorf("got %+v, want a duplicate", res)
	}
	// Redelivered after its order was closed: the order it would open is rolled back.
	closeOrder(t, s, first.WorkOrderID)
	if res := mustStore(t, s, a); !res.Duplicate {
		t.Errorf("after closing: got %+v, want a duplicate", res)
	}
	if got, want := state(t, s, machineID), "concluida [null>aberta] 1 alerts"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStoreAlertAfterOrderClosed(t *testing.T) {
	s, machineID := testStore(t)
	first := mustStore(t, s, newAlert(machineID, contract.AlertHighTemperature))
	closeOrder(t, s, first.WorkOrderID)
	if res := mustStore(t, s, newAlert(machineID, contract.AlertHighTemperature)); !res.Opened {
		t.Errorf("got %+v, want a new order", res)
	}
	want := "concluida [null>aberta] 1 alerts; aberta [null>aberta] 1 alerts"
	if got := state(t, s, machineID); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStoreAlertUnknownMachine(t *testing.T) {
	s, machineID := testStore(t)
	_, err := s.StoreAlert(context.Background(), newAlert(machineID+"-unregistered", contract.AlertHighTemperature))
	reason := rejection(err)
	if reason == nil || !strings.Contains(reason.Error(), "(SQLSTATE 23503)") {
		t.Fatalf("got %v, want a foreign key violation", err)
	}
	var orders int
	if err := s.pg.QueryRow(context.Background(), "SELECT count(*) FROM work_orders WHERE machine_id = $1",
		machineID+"-unregistered").Scan(&orders); err != nil || orders != 0 {
		t.Errorf("got %d orders, %v; want none", orders, err)
	}
}

// A PATCH closing the order while an alert arrives: the alert waits for it and
// opens a new order instead of joining the closed one.
func TestStoreAlertWaitsForOrderBeingClosed(t *testing.T) {
	s, machineID := testStore(t)
	ctx := context.Background()
	first := mustStore(t, s, newAlert(machineID, contract.AlertHighVibration))

	tx, err := s.pg.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "UPDATE work_orders SET status = 'concluida', closed_at = now() WHERE id = $1", first.WorkOrderID)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		res stored
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := s.StoreAlert(ctx, newAlert(machineID, contract.AlertHighVibration))
		done <- result{res, err}
	}()
	waitForLock(t, s, first.WorkOrderID)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !r.res.Opened || r.res.WorkOrderID == first.WorkOrderID {
		t.Errorf("got %+v, want a new order", r.res)
	}
	want := "concluida [null>aberta] 1 alerts; aberta [null>aberta] 1 alerts"
	if got := state(t, s, machineID); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// waitForLock waits until a session is blocked locking the given order with FOR UPDATE.
func waitForLock(t *testing.T, s pgStore, workOrderID int64) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var waiting bool
		err := s.pg.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%FOR UPDATE%')`).
			Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
	}
	t.Fatalf("no session waited for order %d", workOrderID)
}

func mustUpdate(t *testing.T, s pgStore, id int64, c change) event {
	t.Helper()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	e, err := s.UpdateWorkOrder(context.Background(), id, c)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func ptr[T any](v T) *T { return &v }

func TestUpdateWorkOrder(t *testing.T) {
	s, machineID := testStore(t)
	ctx := context.Background()
	a := newAlert(machineID, contract.AlertHighTemperature)
	id := mustStore(t, s, a).WorkOrderID

	// Only the assignee: the event keeps the status.
	e := mustUpdate(t, s, id, change{Assignee: ptr("tecnico-1")})
	if *e.FromStatus != statusOpen || e.ToStatus != statusOpen || *e.Assignee != "tecnico-1" {
		t.Errorf("got %+v, want aberta to aberta with tecnico-1", e)
	}
	// The assignee stays when only the status changes.
	mustUpdate(t, s, id, change{Status: ptr(statusInProgress)})
	done := mustUpdate(t, s, id, change{Status: ptr(statusDone)})

	_, err := s.UpdateWorkOrder(ctx, id, change{Assignee: ptr("tecnico-2")})
	if conflict := (*conflictError)(nil); !errors.As(err, &conflict) {
		t.Errorf("changing a concluded order: got %v, want a conflict", err)
	}

	d, err := s.WorkOrder(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != statusDone || d.Assignee == nil || *d.Assignee != "tecnico-1" || d.AlertCount != 1 {
		t.Errorf("got %+v, want concluida, tecnico-1 and 1 alert", d.workOrder)
	}
	if d.ClosedAt == nil || !d.ClosedAt.Equal(done.CreatedAt) {
		t.Errorf("closed_at %v, want the time of the last event, %v", d.ClosedAt, done.CreatedAt)
	}
	var history []string
	for _, e := range d.Events {
		from, assignee := "null", "null"
		if e.FromStatus != nil {
			from = string(*e.FromStatus)
		}
		if e.Assignee != nil {
			assignee = *e.Assignee
		}
		history = append(history, from+">"+string(e.ToStatus)+" "+assignee)
	}
	want := "[null>aberta null aberta>aberta tecnico-1 aberta>em_andamento tecnico-1 em_andamento>concluida tecnico-1]"
	if got := fmt.Sprint(history); got != want {
		t.Errorf("events %s, want %s", got, want)
	}
	if len(d.Alerts) != 1 || d.Alerts[0].AlertID != a.AlertID || !d.Alerts[0].TS.Equal(a.TS) ||
		d.Alerts[0].Severity != a.Severity || d.Alerts[0].Value != a.Value || d.Alerts[0].Threshold != a.Threshold {
		t.Errorf("alerts %+v, want %+v", d.Alerts, a)
	}
	if d.OpenedAt.Location() != time.UTC || d.OpenedAt.Nanosecond()%int(time.Millisecond) != 0 {
		t.Errorf("opened_at %v, want UTC in milliseconds", d.OpenedAt)
	}
}

func TestWorkOrderNotFound(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.WorkOrder(ctx, 1<<62); !errors.Is(err, errWorkOrderNotFound) {
		t.Errorf("WorkOrder: got %v, want errWorkOrderNotFound", err)
	}
	if _, err := s.UpdateWorkOrder(ctx, 1<<62, change{Status: ptr(statusCanceled)}); !errors.Is(err, errWorkOrderNotFound) {
		t.Errorf("UpdateWorkOrder: got %v, want errWorkOrderNotFound", err)
	}
}

func TestWorkOrders(t *testing.T) {
	s, machineID := testStore(t)
	temp := mustStore(t, s, newAlert(machineID, contract.AlertHighTemperature)).WorkOrderID
	mustStore(t, s, newAlert(machineID, contract.AlertHighTemperature))
	vib := mustStore(t, s, newAlert(machineID, contract.AlertHighVibration)).WorkOrderID
	mustUpdate(t, s, vib, change{Status: ptr(statusInProgress)})

	tests := []struct {
		filter filter
		want   string
	}{
		{filter{MachineID: machineID}, fmt.Sprintf("[%d em_andamento 1 %d aberta 2]", vib, temp)},
		{filter{MachineID: machineID, Status: statusOpen}, fmt.Sprintf("[%d aberta 2]", temp)},
		{filter{MachineID: machineID, Status: statusDone}, "[]"},
	}
	for _, tt := range tests {
		orders, err := s.WorkOrders(context.Background(), tt.filter)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, o := range orders {
			if o.MachineID != machineID {
				t.Errorf("order of machine %s", o.MachineID)
			}
			got = append(got, fmt.Sprintf("%d %s %d", o.ID, o.Status, o.AlertCount))
		}
		if fmt.Sprint(got) != tt.want {
			t.Errorf("%+v: got %v, want %s", tt.filter, got, tt.want)
		}
	}
	// No machine filter: the other machines' orders come too.
	orders, err := s.WorkOrders(context.Background(), filter{Status: statusInProgress})
	if err != nil || !slices.ContainsFunc(orders, func(o workOrder) bool { return o.ID == vib }) {
		t.Errorf("got %d orders, %v; want order %d among them", len(orders), err, vib)
	}
}

// Two people closing the same order: the second waits for the first and gets a conflict.
func TestUpdateWorkOrderWaitsForAnotherChange(t *testing.T) {
	s, machineID := testStore(t)
	ctx := context.Background()
	id := mustStore(t, s, newAlert(machineID, contract.AlertLongStop)).WorkOrderID

	tx, err := s.pg.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "UPDATE work_orders SET status = 'cancelada', closed_at = now() WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.UpdateWorkOrder(ctx, id, change{Status: ptr(statusInProgress)})
		done <- err
	}()
	waitForLock(t, s, id)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err = <-done
	if conflict := (*conflictError)(nil); !errors.As(err, &conflict) || !strings.Contains(err.Error(), "already cancelada") {
		t.Errorf("got %v, want a conflict with cancelada", err)
	}
}
