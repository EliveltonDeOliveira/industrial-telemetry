package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

const maxFaultDurationS = 3600

type faultRequest struct {
	MachineID string             `json:"machine_id"`
	Type      contract.AlertType `json:"type"`
	DurationS int                `json:"duration_s"`
}

type faultResponse struct {
	MachineID string             `json:"machine_id"`
	Type      contract.AlertType `json:"type"`
	Until     time.Time          `json:"until"`
}

// faultsHandler serves POST /faults, which injects a fault into one machine:
//
//	{"machine_id": "maq-01", "type": "temperatura_alta", "duration_s": 120}
//
// The fault types are the alert types the anomaly service should raise.
func faultsHandler(machines map[string]*machine, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req faultRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
		// Unlike the message contract, the API rejects unknown fields, so a typo
		// such as "duration" fails instead of being ignored.
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
			return
		}
		// Checked before converting, so a huge value cannot overflow into a valid duration.
		if req.DurationS < 1 || req.DurationS > maxFaultDurationS {
			writeError(w, http.StatusBadRequest, fmt.Errorf("duration_s: must be between 1 and %d", maxFaultDurationS))
			return
		}
		m, ok := machines[req.MachineID]
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("machine_id: unknown machine %q", req.MachineID))
			return
		}
		until, err := m.inject(req.Type, time.Duration(req.DurationS)*time.Second, now())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		slog.Info("fault injected", "machine_id", m.id, "type", req.Type, "until", until.UTC())
		writeJSON(w, http.StatusAccepted, faultResponse{MachineID: m.id, Type: req.Type, Until: until.UTC().Truncate(time.Millisecond)})
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
