package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// serialTS is when the gateway received the line in these tests.
var serialTS = time.Date(2026, 11, 8, 21, 13, 0, 123_000_000, time.UTC)

// TestSerialExamples checks that every valid line becomes a valid telemetry message
// with its fields unchanged, and that every invalid case is rejected on the expected
// field once the gateway has completed it.
func TestSerialExamples(t *testing.T) {
	dir := filepath.Join(contractsDir, "examples", "serial")

	paths, err := filepath.Glob(filepath.Join(dir, "valid", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no valid examples in %s: %v", dir, err)
	}
	for _, path := range paths {
		t.Run("valid/"+filepath.Base(path), func(t *testing.T) {
			line := readFile(t, path)
			// The firmware test compares its output with these files, so each one is
			// exactly one line as the firmware prints it.
			if bytes.Count(line, []byte("\n")) != 1 || !bytes.HasSuffix(line, []byte("\n")) {
				t.Errorf("example must be a single line ending in \\n")
			}
			if len(line) > MaxSerialLineBytes {
				t.Errorf("example has %d bytes, more than %d", len(line), MaxSerialLineBytes)
			}

			message, err := CompleteSerialLine(line, "maq-04", serialTS)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ParseTelemetry(message)
			if err != nil {
				t.Fatalf("completed line is not valid telemetry: %v", err)
			}
			if got.MachineID != "maq-04" || !got.TS.Equal(serialTS) {
				t.Errorf("machine_id = %q, ts = %v; want maq-04 and %v", got.MachineID, got.TS, serialTS)
			}

			var in, out map[string]json.RawMessage
			if err := json.Unmarshal(line, &in); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(message, &out); err != nil {
				t.Fatal(err)
			}
			for name, value := range in {
				if !bytes.Equal(out[name], value) {
					t.Errorf("%s = %s, the line had %s", name, out[name], value)
				}
			}
		})
	}

	var cases []invalidCase
	if err := json.Unmarshal(readFile(t, filepath.Join(dir, "invalid.json")), &cases); err != nil || len(cases) == 0 {
		t.Fatalf("no invalid cases in %s: %v", dir, err)
	}
	for _, c := range cases {
		t.Run("invalid/"+c.Case, func(t *testing.T) {
			message, err := CompleteSerialLine(c.Message, "maq-04", serialTS)
			if err != nil {
				t.Fatalf("the gateway completes any JSON object, got %v", err)
			}
			_, err = ParseTelemetry(message)
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != c.Field {
				t.Errorf("expected an error on %q, got %v", c.Field, err)
			}
		})
	}
}

func TestCompleteSerialLine(t *testing.T) {
	const reading = `"schema_version":1,"boot_id":7,"seq":3,"state":"running","good_total":2,"scrap_total":0,"temperature_c":30.50,"vibration_rms":0.401`

	tests := []struct {
		name string
		line string
		ts   time.Time
		want string
	}{
		{
			name: "crlf ending",
			line: "{" + reading + "}\r\n",
			ts:   serialTS,
			want: `{"boot_id":7,"good_total":2,"machine_id":"maq-04","schema_version":1,"scrap_total":0,"seq":3,"state":"running","temperature_c":30.50,"ts":"2026-11-08T21:13:00.123Z","vibration_rms":0.401}`,
		},
		{
			name: "machine_id and ts from the line are replaced",
			line: `{` + reading + `,"machine_id":"maq-99","ts":"2020-01-01T00:00:00Z"}`,
			ts:   serialTS,
			want: `{"boot_id":7,"good_total":2,"machine_id":"maq-04","schema_version":1,"scrap_total":0,"seq":3,"state":"running","temperature_c":30.50,"ts":"2026-11-08T21:13:00.123Z","vibration_rms":0.401}`,
		},
		{
			name: "ts in UTC truncated to milliseconds",
			line: "{" + reading + "}\n",
			ts:   time.Date(2026, 11, 8, 18, 13, 0, 123_456_789, time.FixedZone("-03", -3*3600)),
			want: `{"boot_id":7,"good_total":2,"machine_id":"maq-04","schema_version":1,"scrap_total":0,"seq":3,"state":"running","temperature_c":30.50,"ts":"2026-11-08T21:13:00.123Z","vibration_rms":0.401}`,
		},
		{
			name: "unknown field is kept",
			line: `{` + reading + `,"firmware_version":"0.1.0"}`,
			ts:   serialTS,
			want: `{"boot_id":7,"firmware_version":"0.1.0","good_total":2,"machine_id":"maq-04","schema_version":1,"scrap_total":0,"seq":3,"state":"running","temperature_c":30.50,"ts":"2026-11-08T21:13:00.123Z","vibration_rms":0.401}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CompleteSerialLine([]byte(tt.line), "maq-04", tt.ts)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestCompleteSerialLineRejectsBadLines(t *testing.T) {
	for _, line := range []string{
		"",
		"\r\n",
		`ing","good_total":2,"scrap_total":0}` + "\n", // the first line read can start mid-line
		`{"schema_version":1,"boot_id":28`,
		"*** Booting Zephyr OS build v4.3.0 ***\r\n",
		`[]`,
		`null`,
		`42`,
		`"maq-04"`,
		`{"schema_version":1,"firmware_version":"` + strings.Repeat("x", MaxSerialLineBytes) + `"}`,
	} {
		if _, err := CompleteSerialLine([]byte(line), "maq-04", serialTS); err == nil {
			t.Errorf("CompleteSerialLine(%q): expected an error", line)
		}
	}
}
