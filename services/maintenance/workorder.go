package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

// status is the state of a work order, the work_order_status enum of PostgreSQL.
type status string

const (
	statusOpen       status = "aberta"
	statusInProgress status = "em_andamento"
	statusDone       status = "concluida"
	statusCanceled   status = "cancelada"
)

var statuses = []status{statusOpen, statusInProgress, statusDone, statusCanceled}

// transitions lists where each status can go. The final states go nowhere.
var transitions = map[status][]status{
	statusOpen:       {statusInProgress, statusCanceled},
	statusInProgress: {statusDone, statusCanceled},
}

func (s status) valid() bool { return slices.Contains(statuses, s) }

// final reports whether the order is closed: concluida or cancelada.
func (s status) final() bool { return len(transitions[s]) == 0 }

const maxAssigneeLen = 100

// workOrder is a work order as the API returns it.
type workOrder struct {
	ID         int64              `json:"id"`
	MachineID  string             `json:"machine_id"`
	AlertType  contract.AlertType `json:"alert_type"`
	Status     status             `json:"status"`
	Assignee   *string            `json:"assignee"`  // null until someone is assigned
	OpenedAt   time.Time          `json:"opened_at"` // when maintenance opened it, not when the alert fired
	ClosedAt   *time.Time         `json:"closed_at"` // null until concluida or cancelada
	AlertCount int                `json:"alert_count"`
}

// workOrderDetail adds the history and the alerts of an order.
type workOrderDetail struct {
	workOrder
	Events []event      `json:"events"`
	Alerts []orderAlert `json:"alerts"`
}

// event is one row of work_order_events.
type event struct {
	FromStatus *status   `json:"from_status"` // null when the order was opened
	ToStatus   status    `json:"to_status"`
	Assignee   *string   `json:"assignee"`
	CreatedAt  time.Time `json:"created_at"`
}

// orderAlert is an alert linked to an order. Its type and machine are the order's.
type orderAlert struct {
	AlertID    string            `json:"alert_id"`
	TS         time.Time         `json:"ts"`
	Severity   contract.Severity `json:"severity"`
	Value      float64           `json:"value"`
	Threshold  float64           `json:"threshold"`
	ReceivedAt time.Time         `json:"received_at"`
}

// filter selects work orders. An empty field matches any value.
type filter struct {
	Status    status
	MachineID string
}

// change is the body of PATCH /work-orders/{id}. A field left out, or null,
// is not changed.
type change struct {
	Status   *status `json:"status"`
	Assignee *string `json:"assignee"`
}

// validate checks the change on its own, before looking at the order, and
// trims the assignee. Its errors are HTTP 400.
func (c *change) validate() error {
	if c.Status == nil && c.Assignee == nil {
		return errors.New("status: send status, assignee or both")
	}
	if c.Status != nil && !c.Status.valid() {
		return fmt.Errorf("status: %q is not one of %v", *c.Status, statuses)
	}
	if c.Assignee != nil {
		a := strings.TrimSpace(*c.Assignee)
		if a == "" || utf8.RuneCountInString(a) > maxAssigneeLen {
			return fmt.Errorf("assignee: must have 1 to %d characters", maxAssigneeLen)
		}
		c.Assignee = &a
	}
	return nil
}

// conflictError is a change the current status of the order does not allow: HTTP 409.
type conflictError struct{ msg string }

func (e *conflictError) Error() string { return e.msg }

// next returns the status of an order in status current after the change.
// A concluded or canceled order does not change anymore, not even its
// assignee, and the same status again is not a transition (ADR 0014).
func (c change) next(current status) (status, error) {
	if current.final() {
		return "", &conflictError{fmt.Sprintf("status: work order is already %s", current)}
	}
	if c.Status == nil {
		return current, nil
	}
	if !slices.Contains(transitions[current], *c.Status) {
		return "", &conflictError{fmt.Sprintf("status: cannot go from %s to %s, only to %v",
			current, *c.Status, transitions[current])}
	}
	return *c.Status, nil
}
