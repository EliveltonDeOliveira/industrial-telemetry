package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

// stored is what storing one alert did.
type stored struct {
	WorkOrderID int64 // the order the alert opened or joined
	Opened      bool  // the alert opened a new order
	Duplicate   bool  // the alert_id was already stored, so nothing changed
}

// pgStore writes alerts and work orders to PostgreSQL.
type pgStore struct {
	pg *pgxpool.Pool
}

// StoreAlert stores an alert linked to the active work order of its machine and
// alert type, opening one if there is none, in a single transaction (ADR 0013).
// An alert_id already stored, as after a redelivery, rolls back and changes nothing.
func (s pgStore) StoreAlert(ctx context.Context, a contract.Alert) (stored, error) {
	tx, err := s.pg.Begin(ctx)
	if err != nil {
		return stored{}, err
	}
	// After a commit, the rollback does nothing.
	defer tx.Rollback(ctx)

	var res stored
	// FOR UPDATE waits for a transaction changing the same order, such as a
	// PATCH closing it, and then reads its new status: an order closed meanwhile
	// no longer matches, so the alert opens a new one instead of joining it.
	err = tx.QueryRow(ctx, `SELECT id FROM work_orders
		WHERE machine_id = $1 AND alert_type = $2 AND status IN ('aberta', 'em_andamento')
		FOR UPDATE`, a.MachineID, a.Type).Scan(&res.WorkOrderID)
	if errors.Is(err, pgx.ErrNoRows) {
		res.Opened = true
		err = tx.QueryRow(ctx, "INSERT INTO work_orders (machine_id, alert_type) VALUES ($1, $2) RETURNING id",
			a.MachineID, a.Type).Scan(&res.WorkOrderID)
		if err == nil {
			_, err = tx.Exec(ctx, "INSERT INTO work_order_events (work_order_id, to_status) VALUES ($1, 'aberta')",
				res.WorkOrderID)
		}
	}
	if err != nil {
		return stored{}, err
	}

	tag, err := tx.Exec(ctx, `INSERT INTO alerts
		(alert_id, machine_id, ts, alert_type, severity, value, threshold, work_order_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (alert_id) DO NOTHING`,
		a.AlertID, a.MachineID, a.TS, a.Type, a.Severity, a.Value, a.Threshold, res.WorkOrderID)
	if err != nil {
		return stored{}, err
	}
	if tag.RowsAffected() == 0 {
		// The deferred rollback undoes the order opened above, if any.
		return stored{Duplicate: true}, nil
	}
	return res, tx.Commit(ctx)
}

// rejection returns why PostgreSQL refused an alert because of its data, as
// the text for the DLQ, or nil when err means the database is unavailable
// (ADR 0013). The data is at fault when the error has SQLSTATE class 22, data
// exception, or 23, integrity constraint violation, such as 23503 for a machine
// that is not registered. Any other error, such as a lost connection, a timeout
// or the server shutting down, is infrastructure.
func rejection(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || !(strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23")) {
		return nil
	}
	msg := pgErr.Message
	if pgErr.Detail != "" {
		msg += ": " + pgErr.Detail
	}
	return fmt.Errorf("postgres: %s (SQLSTATE %s)", msg, pgErr.Code)
}

var errWorkOrderNotFound = errors.New("work order not found")

// orderColumns are the columns of workOrder, read from work_orders o.
const orderColumns = `o.id, o.machine_id, o.alert_type::text, o.status::text, o.assignee, o.opened_at, o.closed_at,
	(SELECT count(*) FROM alerts a WHERE a.work_order_id = o.id)`

// WorkOrders returns the orders that match f, newest first.
func (s pgStore) WorkOrders(ctx context.Context, f filter) ([]workOrder, error) {
	rows, err := s.pg.Query(ctx, `SELECT `+orderColumns+` FROM work_orders o
		WHERE ($1 = '' OR o.status::text = $1) AND ($2 = '' OR o.machine_id = $2)
		ORDER BY o.id DESC`, string(f.Status), f.MachineID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanWorkOrder)
}

// WorkOrder returns an order with its events and alerts, oldest first. The
// three queries run in one read only transaction, so they see the same moment.
func (s pgStore) WorkOrder(ctx context.Context, id int64) (workOrderDetail, error) {
	var d workOrderDetail
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	err := pgx.BeginTxFunc(ctx, s.pg, opts, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+orderColumns+` FROM work_orders o WHERE o.id = $1`, id)
		if err != nil {
			return err
		}
		d.workOrder, err = pgx.CollectExactlyOneRow(rows, scanWorkOrder)
		if errors.Is(err, pgx.ErrNoRows) {
			return errWorkOrderNotFound
		}
		if err != nil {
			return err
		}

		rows, err = tx.Query(ctx, `SELECT from_status::text, to_status::text, assignee, created_at
			FROM work_order_events WHERE work_order_id = $1 ORDER BY id`, id)
		if err != nil {
			return err
		}
		d.Events, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (event, error) {
			var e event
			err := row.Scan(&e.FromStatus, &e.ToStatus, &e.Assignee, &e.CreatedAt)
			e.CreatedAt = utcMillis(e.CreatedAt)
			return e, err
		})
		if err != nil {
			return err
		}

		rows, err = tx.Query(ctx, `SELECT alert_id::text, ts, severity::text, value, threshold, received_at
			FROM alerts WHERE work_order_id = $1 ORDER BY ts, alert_id`, id)
		if err != nil {
			return err
		}
		d.Alerts, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (orderAlert, error) {
			var a orderAlert
			err := row.Scan(&a.AlertID, &a.TS, &a.Severity, &a.Value, &a.Threshold, &a.ReceivedAt)
			a.TS, a.ReceivedAt = utcMillis(a.TS), utcMillis(a.ReceivedAt)
			return a, err
		})
		return err
	})
	return d, err
}

// UpdateWorkOrder applies a change to an order and records it in
// work_order_events, in one transaction, and returns the event. FOR UPDATE
// makes two changes to the same order, or a change and an alert joining it,
// wait for each other, so each one sees the status the other left (ADR 0014).
// A change the current status does not allow returns a *conflictError.
func (s pgStore) UpdateWorkOrder(ctx context.Context, id int64, c change) (event, error) {
	tx, err := s.pg.Begin(ctx)
	if err != nil {
		return event{}, err
	}
	defer tx.Rollback(ctx)

	var current status
	var assignee *string
	err = tx.QueryRow(ctx, "SELECT status::text, assignee FROM work_orders WHERE id = $1 FOR UPDATE", id).
		Scan(&current, &assignee)
	if errors.Is(err, pgx.ErrNoRows) {
		return event{}, errWorkOrderNotFound
	}
	if err != nil {
		return event{}, err
	}
	next, err := c.next(current)
	if err != nil {
		return event{}, err
	}
	if c.Assignee != nil {
		assignee = c.Assignee
	}

	// closed_at is set when the order reaches a final state, with the same
	// now() as the event, since now() is the start of the transaction.
	_, err = tx.Exec(ctx, `UPDATE work_orders SET status = $2, assignee = $3,
		closed_at = CASE WHEN $4 THEN now() END
		WHERE id = $1`, id, next, assignee, next.final())
	if err != nil {
		return event{}, err
	}
	e := event{FromStatus: &current, ToStatus: next, Assignee: assignee}
	err = tx.QueryRow(ctx, `INSERT INTO work_order_events (work_order_id, from_status, to_status, assignee)
		VALUES ($1, $2, $3, $4) RETURNING created_at`, id, current, next, assignee).Scan(&e.CreatedAt)
	if err != nil {
		return event{}, err
	}
	e.CreatedAt = utcMillis(e.CreatedAt)
	return e, tx.Commit(ctx)
}

func scanWorkOrder(row pgx.CollectableRow) (workOrder, error) {
	var o workOrder
	err := row.Scan(&o.ID, &o.MachineID, &o.AlertType, &o.Status, &o.Assignee, &o.OpenedAt, &o.ClosedAt, &o.AlertCount)
	o.OpenedAt = utcMillis(o.OpenedAt)
	if o.ClosedAt != nil {
		closed := utcMillis(*o.ClosedAt)
		o.ClosedAt = &closed
	}
	return o, err
}

// utcMillis gives the API the same time format as the contract: UTC with
// milliseconds. PostgreSQL keeps microseconds.
func utcMillis(t time.Time) time.Time {
	return t.UTC().Truncate(time.Millisecond)
}
