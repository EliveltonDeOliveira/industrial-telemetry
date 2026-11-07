package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFaultsHandler(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
		want   string // part of the response body
	}{
		{"valid", `{"machine_id":"maq-01","type":"parada_longa","duration_s":360}`, http.StatusAccepted, `"until":"2026-11-07T14:06:00Z"`},
		{"malformed JSON", `{"machine_id":`, http.StatusBadRequest, "invalid JSON"},
		{"unknown field", `{"machine_id":"maq-01","type":"parada_longa","duration":360}`, http.StatusBadRequest, "unknown field"},
		{"missing duration", `{"machine_id":"maq-01","type":"parada_longa"}`, http.StatusBadRequest, "duration_s:"},
		{"duration too long", `{"machine_id":"maq-01","type":"parada_longa","duration_s":3601}`, http.StatusBadRequest, "duration_s:"},
		{"unknown machine", `{"machine_id":"maq-99","type":"parada_longa","duration_s":360}`, http.StatusNotFound, "machine_id:"},
		{"unknown type", `{"machine_id":"maq-01","type":"incendio","duration_s":360}`, http.StatusBadRequest, "type:"},
	}
	machines := map[string]*machine{"maq-01": newTestMachine(noStops)}
	handler := faultsHandler(machines, func() time.Time { return t0 })
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/faults", strings.NewReader(tt.body)))
			if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("got %d %s, want %d with %s", rec.Code, rec.Body, tt.status, tt.want)
			}
		})
	}
}
