package contract

import (
	"encoding/json"
	"maps"
	"math"
	"path/filepath"
	"slices"
	"testing"
)

// TestSchemasMatchGo checks that the JSON Schemas in contracts/ declare the same
// fields, enums and limits that this package enforces.
func TestSchemasMatchGo(t *testing.T) {
	tests := []struct {
		file     string
		fields   []field
		keywords map[string]map[string]any // property -> schema keyword -> value enforced in Go
	}{
		{
			file:   "telemetry.schema.json",
			fields: (&Telemetry{}).fields(),
			keywords: map[string]map[string]any{
				"schema_version": {"const": SchemaVersion},
				"machine_id":     {"pattern": machineIDPattern.String()},
				"boot_id":        {"minimum": 0, "maximum": math.MaxUint32},
				"state":          {"enum": states},
				"temperature_c":  {"minimum": MinTemperatureC, "maximum": MaxTemperatureC},
				"vibration_rms":  {"minimum": 0, "maximum": MaxVibrationRMS},
			},
		},
		{
			// The serial line is the telemetry without the fields the gateway sets (ADR 0020).
			file: "serial.schema.json",
			fields: slices.DeleteFunc((&Telemetry{}).fields(), func(f field) bool {
				return f.name == "machine_id" || f.name == "ts"
			}),
			keywords: map[string]map[string]any{
				"schema_version": {"const": SchemaVersion},
				"boot_id":        {"minimum": 0, "maximum": math.MaxUint32},
				"state":          {"enum": states},
				"temperature_c":  {"minimum": MinTemperatureC, "maximum": MaxTemperatureC},
				"vibration_rms":  {"minimum": 0, "maximum": MaxVibrationRMS},
			},
		},
		{
			file:   "alert.schema.json",
			fields: (&Alert{}).fields(),
			keywords: map[string]map[string]any{
				"schema_version": {"const": SchemaVersion},
				"alert_id":       {"pattern": uuidPattern.String()},
				"machine_id":     {"pattern": machineIDPattern.String()},
				"type":           {"enum": alertTypes},
				"severity":       {"enum": severities},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			var schema struct {
				Required   []string                  `json:"required"`
				Properties map[string]map[string]any `json:"properties"`
			}
			if err := json.Unmarshal(readFile(t, filepath.Join(contractsDir, tt.file)), &schema); err != nil {
				t.Fatal(err)
			}

			var names []string
			for _, f := range tt.fields {
				names = append(names, f.name)
			}
			slices.Sort(names)
			if got := slices.Sorted(slices.Values(schema.Required)); !slices.Equal(got, names) {
				t.Errorf("required = %v, Go decodes %v", got, names)
			}
			if got := slices.Sorted(maps.Keys(schema.Properties)); !slices.Equal(got, names) {
				t.Errorf("properties = %v, Go decodes %v", got, names)
			}

			for prop, keywords := range tt.keywords {
				for keyword, want := range keywords {
					got := schema.Properties[prop][keyword]
					if toJSON(t, got) != toJSON(t, want) {
						t.Errorf("%s.%s = %s, Go enforces %s", prop, keyword, toJSON(t, got), toJSON(t, want))
					}
				}
			}
		})
	}
}

// toJSON lets a value decoded from the schema be compared with a Go value of another type.
func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
