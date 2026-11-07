package contract

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// contractsDir holds the schemas and examples shared by the Go and Python tests.
var contractsDir = filepath.Join("..", "..", "contracts")

// invalidCase is one entry of contracts/examples/<kind>/invalid.json.
type invalidCase struct {
	Case    string          `json:"case"`
	Field   string          `json:"field"`
	Message json.RawMessage `json:"message"`
}

func TestTelemetryExamples(t *testing.T) {
	testExamples(t, "telemetry", ParseTelemetry)
}

func TestAlertExamples(t *testing.T) {
	testExamples(t, "alert", ParseAlert)
}

// testExamples checks that every valid example parses and survives a JSON round trip,
// and that every invalid case is rejected with an error on the expected field.
func testExamples[T comparable](t *testing.T, kind string, parse func([]byte) (T, error)) {
	dir := filepath.Join(contractsDir, "examples", kind)

	paths, err := filepath.Glob(filepath.Join(dir, "valid", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no valid examples in %s: %v", dir, err)
	}
	for _, path := range paths {
		t.Run("valid/"+filepath.Base(path), func(t *testing.T) {
			got, err := parse(readFile(t, path))
			if err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			again, err := parse(encoded)
			if err != nil {
				t.Fatalf("round trip: %v", err)
			}
			if again != got {
				t.Errorf("round trip changed the message:\n got %+v\nwant %+v", again, got)
			}
		})
	}

	var cases []invalidCase
	if err := json.Unmarshal(readFile(t, filepath.Join(dir, "invalid.json")), &cases); err != nil || len(cases) == 0 {
		t.Fatalf("no invalid cases in %s: %v", dir, err)
	}
	for _, c := range cases {
		t.Run("invalid/"+c.Case, func(t *testing.T) {
			_, err := parse(c.Message)
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != c.Field {
				t.Errorf("expected an error on %q, got %v", c.Field, err)
			}
		})
	}
}

func TestParseTelemetry(t *testing.T) {
	tests := []struct {
		file string
		want Telemetry
	}{
		{
			file: "running.json",
			want: Telemetry{
				SchemaVersion: 1, MachineID: "maq-01", BootID: 2849120113, Seq: 1042,
				TS: time.Date(2026, 11, 7, 14, 3, 0, 0, time.UTC), State: StateRunning,
				GoodTotal: 812, ScrapTotal: 9, TemperatureC: 31.5, VibrationRMS: 0.42,
			},
		},
		{
			file: "ts-with-offset.json",
			want: Telemetry{
				SchemaVersion: 1, MachineID: "maq-02", BootID: 1593024788, Seq: 57,
				TS: time.Date(2026, 11, 7, 14, 3, 0, 0, time.UTC), State: StateRunning,
				GoodTotal: 40, ScrapTotal: 1, TemperatureC: 29.8, VibrationRMS: 0.38,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got, err := ParseTelemetry(readFile(t, filepath.Join(contractsDir, "examples", "telemetry", "valid", tt.file)))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestParseAlert(t *testing.T) {
	got, err := ParseAlert(readFile(t, filepath.Join(contractsDir, "examples", "alert", "valid", "temperatura-alta.json")))
	if err != nil {
		t.Fatal(err)
	}
	want := Alert{
		SchemaVersion: 1, AlertID: "8f2c1a7e-3b1d-4c6e-9a0f-2d5b7e9c1a44", MachineID: "maq-01",
		TS: time.Date(2026, 11, 7, 14, 5, 0, 0, time.UTC), Type: AlertHighTemperature,
		Severity: SeverityHigh, Value: 78.2, Threshold: 65.0,
	}
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseRejectsMalformedJSON(t *testing.T) {
	for _, input := range []string{``, `{`, `[]`, `"maq-01"`, `42`, `null`} {
		if _, err := ParseTelemetry([]byte(input)); err == nil {
			t.Errorf("ParseTelemetry(%q): expected an error", input)
		}
		if _, err := ParseAlert([]byte(input)); err == nil {
			t.Errorf("ParseAlert(%q): expected an error", input)
		}
	}
}

// Producers build messages in code and call Validate, so values that JSON cannot
// carry, like NaN and infinity, must be rejected too.
func TestValidateRejectsNonFiniteNumbers(t *testing.T) {
	telemetry := Telemetry{
		SchemaVersion: 1, MachineID: "maq-01", TS: time.Now(), State: StateRunning,
		TemperatureC: math.NaN(),
	}
	if err := telemetry.Validate(); err == nil {
		t.Error("Telemetry with NaN temperature: expected an error")
	}

	alert := Alert{
		SchemaVersion: 1, AlertID: "8f2c1a7e-3b1d-4c6e-9a0f-2d5b7e9c1a44", MachineID: "maq-01",
		TS: time.Now(), Type: AlertHighVibration, Severity: SeverityLow, Value: math.Inf(1),
	}
	if err := alert.Validate(); err == nil {
		t.Error("Alert with infinite value: expected an error")
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
