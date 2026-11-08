package httpmetrics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// count returns how many requests were measured with these labels.
func count(t *testing.T, method, route, code string) uint64 {
	t.Helper()
	var m dto.Metric
	if err := requestDuration.WithLabelValues(method, route, code).(prometheus.Metric).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram().GetSampleCount()
}

func TestHandler(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /work-orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "404" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		fmt.Fprintln(w, "ok") // no WriteHeader: 200
	})
	mux.HandleFunc("PATCH /work-orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.WriteHeader(http.StatusOK) // ignored by net/http, and here too
	})
	h := Handler(mux)
	for _, req := range []struct{ method, path string }{
		{"GET", "/work-orders/1"}, {"GET", "/work-orders/2"}, {"GET", "/work-orders/404"},
		{"PATCH", "/work-orders/1"}, {"GET", "/nothing/here"}, {"DELETE", "/work-orders/1"},
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(req.method, req.path, nil))
	}

	tests := []struct {
		method, route, code string
		want                uint64
	}{
		{"GET", "GET /work-orders/{id}", "200", 2},
		{"GET", "GET /work-orders/{id}", "404", 1},
		{"PATCH", "PATCH /work-orders/{id}", "409", 1},
		{"GET", "unmatched", "404", 1},
		{"DELETE", "unmatched", "405", 1},
	}
	for _, tt := range tests {
		if got := count(t, tt.method, tt.route, tt.code); got != tt.want {
			t.Errorf("%s %s %s: %d requests, want %d", tt.method, tt.route, tt.code, got, tt.want)
		}
	}
}
