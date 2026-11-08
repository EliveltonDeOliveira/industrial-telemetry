package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

var opened = time.Date(2026, 11, 8, 18, 1, 39, 179_000_000, time.UTC)

var order = workOrder{ID: 12, MachineID: "maq-03", AlertType: contract.AlertHighVibration,
	Status: statusOpen, OpenedAt: opened, AlertCount: 1}

var orderJSON = `{"id": 12, "machine_id": "maq-03", "alert_type": "vibracao_alta", "status": "aberta",
	"assignee": null, "opened_at": "2026-11-08T18:01:39.179Z", "closed_at": null, "alert_count": 1}`

var detail = workOrderDetail{
	workOrder: order,
	Events:    []event{{ToStatus: statusOpen, CreatedAt: opened}},
	Alerts: []orderAlert{{AlertID: "fe6f9b9b-bfce-57bd-ace2-d7fa326b2425", TS: opened.Add(-time.Hour),
		Severity: contract.SeverityMedium, Value: 3.445, Threshold: 2.243, ReceivedAt: opened}},
}

var detailJSON = strings.TrimSuffix(orderJSON, "}") + `,
	"events": [{"from_status": null, "to_status": "aberta", "assignee": null, "created_at": "2026-11-08T18:01:39.179Z"}],
	"alerts": [{"alert_id": "fe6f9b9b-bfce-57bd-ace2-d7fa326b2425", "ts": "2026-11-08T17:01:39.179Z",
		"severity": "media", "value": 3.445, "threshold": 2.243, "received_at": "2026-11-08T18:01:39.179Z"}]}`

// fakeOrders serves order 12 and records what it was asked.
type fakeOrders struct {
	orders    []workOrder
	updateErr error // returned by UpdateWorkOrder
	err       error // returned by every call

	filter filter
	change *change
}

func (f *fakeOrders) WorkOrders(_ context.Context, flt filter) ([]workOrder, error) {
	f.filter = flt
	return f.orders, f.err
}

func (f *fakeOrders) WorkOrder(_ context.Context, id int64) (workOrderDetail, error) {
	if id != detail.ID {
		return workOrderDetail{}, errWorkOrderNotFound
	}
	return detail, f.err
}

func (f *fakeOrders) UpdateWorkOrder(_ context.Context, id int64, c change) (event, error) {
	if id != detail.ID {
		return event{}, errWorkOrderNotFound
	}
	f.change = &c
	from := statusOpen
	return event{FromStatus: &from, ToStatus: statusInProgress}, errors.Join(f.updateErr, f.err)
}

// serve sends one request through the API routes.
func serve(f *fakeOrders, method, target, body string) (int, string) {
	mux := http.NewServeMux()
	(&api{store: f}).register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

var errDown = errors.New("connection refused")

func TestAPI(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		target, body string
		store        *fakeOrders
		wantStatus   int
		wantBody     string
	}{
		{name: "list", target: "/work-orders", store: &fakeOrders{orders: []workOrder{order}},
			wantStatus: 200, wantBody: `{"work_orders": [` + orderJSON + `]}`},
		{name: "list nothing", target: "/work-orders?machine_id=maq-99", store: &fakeOrders{},
			wantStatus: 200, wantBody: `{"work_orders": []}`},
		{name: "list unknown status", target: "/work-orders?status=fechada", store: &fakeOrders{},
			wantStatus: 400, wantBody: `{"error": "status: \"fechada\" is not one of [aberta em_andamento concluida cancelada]"}`},
		{name: "list failure", target: "/work-orders", store: &fakeOrders{err: errDown},
			wantStatus: 500, wantBody: `{"error": "internal error"}`},

		{name: "get", target: "/work-orders/12", store: &fakeOrders{}, wantStatus: 200, wantBody: detailJSON},
		{name: "get missing", target: "/work-orders/13", store: &fakeOrders{},
			wantStatus: 404, wantBody: `{"error": "work order not found"}`},
		{name: "get invalid id", target: "/work-orders/abc", store: &fakeOrders{},
			wantStatus: 400, wantBody: `{"error": "id: \"abc\" is not a work order id"}`},
		{name: "get id zero", target: "/work-orders/0", store: &fakeOrders{},
			wantStatus: 400, wantBody: `{"error": "id: \"0\" is not a work order id"}`},
		{name: "get failure", target: "/work-orders/12", store: &fakeOrders{err: errDown},
			wantStatus: 500, wantBody: `{"error": "internal error"}`},

		{name: "patch", method: "PATCH", target: "/work-orders/12", body: `{"status": "em_andamento", "assignee": "tecnico-1"}`,
			store: &fakeOrders{}, wantStatus: 200, wantBody: detailJSON},
		{name: "patch missing", method: "PATCH", target: "/work-orders/13", body: `{"status": "em_andamento"}`,
			store: &fakeOrders{}, wantStatus: 404, wantBody: `{"error": "work order not found"}`},
		{name: "patch not allowed", method: "PATCH", target: "/work-orders/12", body: `{"status": "concluida"}`,
			store:      &fakeOrders{updateErr: &conflictError{"status: cannot go from aberta to concluida, only to [em_andamento cancelada]"}},
			wantStatus: 409, wantBody: `{"error": "status: cannot go from aberta to concluida, only to [em_andamento cancelada]"}`},
		{name: "patch nothing", method: "PATCH", target: "/work-orders/12", body: `{}`,
			store: &fakeOrders{}, wantStatus: 400, wantBody: `{"error": "status: send status, assignee or both"}`},
		{name: "patch unknown status", method: "PATCH", target: "/work-orders/12", body: `{"status": "fechada"}`,
			store: &fakeOrders{}, wantStatus: 400,
			wantBody: `{"error": "status: \"fechada\" is not one of [aberta em_andamento concluida cancelada]"}`},
		{name: "patch unknown field", method: "PATCH", target: "/work-orders/12", body: `{"state": "concluida"}`,
			store: &fakeOrders{}, wantStatus: 400, wantBody: `{"error": "invalid JSON: json: unknown field \"state\""}`},
		{name: "patch malformed", method: "PATCH", target: "/work-orders/12", body: `{"status":`,
			store: &fakeOrders{}, wantStatus: 400, wantBody: `{"error": "invalid JSON: unexpected EOF"}`},
		{name: "patch failure", method: "PATCH", target: "/work-orders/12", body: `{"status": "em_andamento"}`,
			store: &fakeOrders{err: errDown}, wantStatus: 500, wantBody: `{"error": "internal error"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			status, body := serve(tt.store, method, tt.target, tt.body)
			if status != tt.wantStatus {
				t.Errorf("status %d, want %d", status, tt.wantStatus)
			}
			assertJSON(t, body, tt.wantBody)
		})
	}
}

func TestListPassesTheFilter(t *testing.T) {
	f := &fakeOrders{}
	serve(f, http.MethodGet, "/work-orders?status=em_andamento&machine_id=maq-03", "")
	if want := (filter{Status: statusInProgress, MachineID: "maq-03"}); f.filter != want {
		t.Errorf("got %+v, want %+v", f.filter, want)
	}
}

func TestPatchPassesTheChange(t *testing.T) {
	f := &fakeOrders{}
	serve(f, http.MethodPatch, "/work-orders/12", `{"assignee": "  tecnico-1 "}`)
	if f.change == nil || f.change.Status != nil || f.change.Assignee == nil || *f.change.Assignee != "tecnico-1" {
		t.Errorf("got %+v, want only the assignee, trimmed", f.change)
	}
}

func TestOtherMethods(t *testing.T) {
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if status, _ := serve(&fakeOrders{}, m, "/work-orders/12", ""); status != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 405", m, status)
		}
	}
}

// assertJSON compares two JSON documents, ignoring formatting.
func assertJSON(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("expected body is not JSON: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("body %s\nwant %s", got, want)
	}
}
