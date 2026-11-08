package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/oee"
)

var now = time.Date(2026, 11, 7, 15, 0, 0, 123_456_789, time.UTC)

func TestParsePeriod(t *testing.T) {
	nowMS := now.Truncate(time.Millisecond)
	tests := []struct {
		name     string
		query    string
		from, to time.Time
		wantErr  string // field the error must start with, empty for no error
	}{
		{name: "defaults to the last hour", from: nowMS.Add(-time.Hour), to: nowMS},
		{name: "from only ends now", query: "from=2026-11-07T12:00:00Z",
			from: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC), to: nowMS},
		{name: "to only starts an hour before", query: "to=2026-11-07T14:00:00Z",
			from: time.Date(2026, 11, 7, 13, 0, 0, 0, time.UTC), to: time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC)},
		{name: "offset is converted to UTC", query: "from=2026-11-07T09:00:00-03:00&to=2026-11-07T10:30:00.5-03:00",
			from: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC), to: time.Date(2026, 11, 7, 13, 30, 0, 500_000_000, time.UTC)},
		{name: "sub-millisecond digits are dropped", query: "from=2026-11-07T14:00:00.0019Z&to=2026-11-07T14:30:00.9999999Z",
			from: time.Date(2026, 11, 7, 14, 0, 0, 1_000_000, time.UTC), to: time.Date(2026, 11, 7, 14, 30, 0, 999_000_000, time.UTC)},
		{name: "to in the future becomes now", query: "from=2026-11-07T14:00:00Z&to=2026-11-08T00:00:00Z",
			from: time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC), to: nowMS},
		{name: "exactly the longest period", query: "from=2026-10-31T14:00:00Z&to=2026-11-07T14:00:00Z",
			from: time.Date(2026, 10, 31, 14, 0, 0, 0, time.UTC), to: time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC)},
		{name: "longer than the longest period", query: "from=2026-10-31T13:59:59Z&to=2026-11-07T14:00:00Z", wantErr: "from"},
		{name: "from after to", query: "from=2026-11-07T14:00:00Z&to=2026-11-07T13:00:00Z", wantErr: "from"},
		{name: "from equal to to", query: "from=2026-11-07T14:00:00Z&to=2026-11-07T14:00:00Z", wantErr: "from"},
		{name: "from in the future", query: "from=2026-11-07T16:00:00Z", wantErr: "from"},
		{name: "date without time", query: "from=2026-11-07", wantErr: "from"},
		{name: "unix timestamp", query: "to=1794063600", wantErr: "to"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatal(err)
			}
			from, to, err := parsePeriod(q, now)
			if tt.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr+":") {
					t.Errorf("got error %v, want one starting with %q", err, tt.wantErr+":")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !from.Equal(tt.from) || !to.Equal(tt.to) || from.Location() != time.UTC || to.Location() != time.UTC {
				t.Errorf("got [%v, %v), want [%v, %v) in UTC", from, to, tt.from, tt.to)
			}
		})
	}
}

// fakeStore serves fixed data and records the period it was asked for.
type fakeStore struct {
	machines []machine
	last     map[string]lastReading
	readings []oee.Reading
	err      error

	from, to time.Time
}

func (f *fakeStore) Machines(context.Context) ([]machine, error) {
	return f.machines, f.err
}

func (f *fakeStore) Machine(_ context.Context, id string) (machine, error) {
	for _, m := range f.machines {
		if m.ID == id {
			return m, f.err
		}
	}
	return machine{}, errMachineNotFound
}

func (f *fakeStore) LastReadings(context.Context) (map[string]lastReading, error) {
	return f.last, f.err
}

func (f *fakeStore) Readings(_ context.Context, _ string, from, to time.Time) ([]oee.Reading, error) {
	f.from, f.to = from, to
	return f.readings, f.err
}

var registry = []machine{
	{ID: "maq-01", Name: "Prensa hidráulica", IdealCycle: 4 * time.Second},
	{ID: "maq-04", Name: "Bancada micro:bit", IdealCycle: 5 * time.Second},
}

// get serves one request through the API routes.
func get(f *fakeStore, target string) (int, string) {
	mux := http.NewServeMux()
	(&api{store: f, now: func() time.Time { return now }}).register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Code, rec.Body.String()
}

func TestListMachines(t *testing.T) {
	ts := time.Date(2026, 11, 7, 14, 59, 59, 500_000_000, time.UTC)
	tests := []struct {
		name       string
		store      *fakeStore
		wantStatus int
		wantBody   string
	}{
		{
			name: "registered machines with their latest reading",
			store: &fakeStore{machines: registry, last: map[string]lastReading{
				"maq-01": {TS: ts, State: contract.StateRunning, TemperatureC: 48.2, VibrationRMS: 0.45},
				"maq-99": {TS: ts, State: contract.StateStopped}, // not registered
			}},
			wantStatus: http.StatusOK,
			wantBody: `{"machines": [
				{"machine_id": "maq-01", "name": "Prensa hidráulica", "ideal_cycle_s": 4,
				 "last_reading": {"ts": "2026-11-07T14:59:59.5Z", "state": "running", "temperature_c": 48.2, "vibration_rms": 0.45}},
				{"machine_id": "maq-04", "name": "Bancada micro:bit", "ideal_cycle_s": 5, "last_reading": null}
			]}`,
		},
		{
			name:       "empty registry is an empty list",
			store:      &fakeStore{},
			wantStatus: http.StatusOK,
			wantBody:   `{"machines": []}`,
		},
		{
			name:       "database failure",
			store:      &fakeStore{machines: registry, err: errors.New("connection refused")},
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"error": "internal error"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := get(tt.store, "/machines")
			if status != tt.wantStatus {
				t.Errorf("status %d, want %d", status, tt.wantStatus)
			}
			assertJSON(t, body, tt.wantBody)
		})
	}
}

func TestMachineOEE(t *testing.T) {
	start := time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC)
	readings := []oee.Reading{
		{BootID: 7, TS: start, State: contract.StateRunning},
		{BootID: 7, TS: start.Add(100 * time.Second), State: contract.StateRunning, GoodTotal: 20, ScrapTotal: 5},
	}
	tests := []struct {
		name       string
		target     string
		store      *fakeStore
		wantStatus int
		wantBody   string
	}{
		{
			name:       "OEE in the period",
			target:     "/machines/maq-01/oee?from=2026-11-07T14:00:00Z&to=2026-11-07T14:01:40Z",
			store:      &fakeStore{machines: registry, readings: readings},
			wantStatus: http.StatusOK,
			wantBody: `{"machine_id": "maq-01", "from": "2026-11-07T14:00:00Z", "to": "2026-11-07T14:01:40Z",
				"planned_s": 100, "running_s": 100, "good": 20, "scrap": 5,
				"availability": 1, "performance": 1, "quality": 0.8, "oee": 0.8}`,
		},
		{
			name:       "machine without readings",
			target:     "/machines/maq-04/oee?from=2026-11-07T14:00:00Z&to=2026-11-07T14:01:40Z",
			store:      &fakeStore{machines: registry},
			wantStatus: http.StatusOK,
			wantBody: `{"machine_id": "maq-04", "from": "2026-11-07T14:00:00Z", "to": "2026-11-07T14:01:40Z",
				"planned_s": 100, "running_s": 0, "good": 0, "scrap": 0,
				"availability": 0, "performance": 0, "quality": 0, "oee": 0}`,
		},
		{
			name:       "machine not registered",
			target:     "/machines/maq-99/oee",
			store:      &fakeStore{machines: registry},
			wantStatus: http.StatusNotFound,
			wantBody:   `{"error": "machine not found"}`,
		},
		{
			name:       "invalid period",
			target:     "/machines/maq-01/oee?from=yesterday",
			store:      &fakeStore{machines: registry},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error": "from: want RFC 3339, such as 2026-11-07T14:00:00Z"}`,
		},
		{
			name:       "database failure",
			target:     "/machines/maq-01/oee",
			store:      &fakeStore{machines: registry, err: errors.New("connection refused")},
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"error": "internal error"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := get(tt.store, tt.target)
			if status != tt.wantStatus {
				t.Errorf("status %d, want %d", status, tt.wantStatus)
			}
			assertJSON(t, body, tt.wantBody)
		})
	}
}

func TestMachineOEEQueriesThePeriod(t *testing.T) {
	f := &fakeStore{machines: registry}
	get(f, "/machines/maq-01/oee?from=2026-11-07T14:00:00Z")
	if want := time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC); !f.from.Equal(want) || !f.to.Equal(now.Truncate(time.Millisecond)) {
		t.Errorf("queried [%v, %v), want [%v, now)", f.from, f.to, want)
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
