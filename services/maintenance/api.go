package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/httpjson"
)

const (
	queryTimeout = 10 * time.Second
	maxBodyBytes = 1 << 10
)

// workOrderStore is what the API needs from pgStore, so tests can replace it.
type workOrderStore interface {
	WorkOrders(ctx context.Context, f filter) ([]workOrder, error)
	WorkOrder(ctx context.Context, id int64) (workOrderDetail, error)
	UpdateWorkOrder(ctx context.Context, id int64, c change) (event, error)
}

// api serves the mini CMMS: work orders are opened by alerts and changed by people.
type api struct {
	store workOrderStore
}

func (a *api) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /work-orders", a.listWorkOrders)
	mux.HandleFunc("GET /work-orders/{id}", a.getWorkOrder)
	mux.HandleFunc("PATCH /work-orders/{id}", a.patchWorkOrder)
}

// listWorkOrders returns the orders, newest first, filtered by the optional
// status and machine_id query parameters.
func (a *api) listWorkOrders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := filter{Status: status(q.Get("status")), MachineID: q.Get("machine_id")}
	if f.Status != "" && !f.Status.valid() {
		httpjson.Error(w, http.StatusBadRequest, fmt.Errorf("status: %q is not one of %v", f.Status, statuses))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	orders, err := a.store.WorkOrders(ctx, f)
	if err != nil {
		httpjson.InternalError(w, r, err)
		return
	}
	if orders == nil {
		orders = []workOrder{} // [] instead of null
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"work_orders": orders})
}

// getWorkOrder returns an order with its history and its alerts.
func (a *api) getWorkOrder(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		httpjson.Error(w, http.StatusBadRequest, err)
		return
	}
	a.writeWorkOrder(w, r, id)
}

// patchWorkOrder changes the status, the assignee or both, as in
// {"status": "em_andamento", "assignee": "tecnico-1"}, and returns the order
// as GET does. A transition the status does not allow is a 409 (ADR 0014).
func (a *api) patchWorkOrder(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		httpjson.Error(w, http.StatusBadRequest, err)
		return
	}
	var c change
	if err := httpjson.Decode(w, r, maxBodyBytes, &c); err != nil {
		httpjson.Error(w, http.StatusBadRequest, err)
		return
	}
	if err := c.validate(); err != nil {
		httpjson.Error(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	e, err := a.store.UpdateWorkOrder(ctx, id, c)
	var conflict *conflictError
	switch {
	case errors.Is(err, errWorkOrderNotFound):
		httpjson.Error(w, http.StatusNotFound, err)
		return
	case errors.As(err, &conflict):
		httpjson.Error(w, http.StatusConflict, err)
		return
	case err != nil:
		httpjson.InternalError(w, r, err)
		return
	}
	slog.Info("work order changed", "work_order_id", id, "from_status", *e.FromStatus, "to_status", e.ToStatus)
	a.writeWorkOrder(w, r, id)
}

func (a *api) writeWorkOrder(w http.ResponseWriter, r *http.Request, id int64) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	d, err := a.store.WorkOrder(ctx, id)
	if errors.Is(err, errWorkOrderNotFound) {
		httpjson.Error(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		httpjson.InternalError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, d)
}

func parseID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("id: %q is not a work order id", r.PathValue("id"))
	}
	return id, nil
}
