package contract

import (
	"regexp"
	"slices"
	"time"
)

// AlertType is the kind of anomaly. The values are part of the contract, in Portuguese.
type AlertType string

const (
	AlertHighTemperature AlertType = "temperatura_alta"
	AlertHighVibration   AlertType = "vibracao_alta"
	AlertLongStop        AlertType = "parada_longa"
)

var alertTypes = []AlertType{AlertHighTemperature, AlertHighVibration, AlertLongStop}

// Severity is how urgent an alert is. The values are part of the contract, in Portuguese.
type Severity string

const (
	SeverityLow    Severity = "baixa"
	SeverityMedium Severity = "media"
	SeverityHigh   Severity = "alta"
)

var severities = []Severity{SeverityLow, SeverityMedium, SeverityHigh}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Alert is an anomaly detected on a machine.
// Value and Threshold use the unit of the alert type: degrees Celsius, g, or seconds stopped.
type Alert struct {
	SchemaVersion int       `json:"schema_version"`
	AlertID       string    `json:"alert_id"`
	MachineID     string    `json:"machine_id"`
	TS            time.Time `json:"ts"`
	Type          AlertType `json:"type"`
	Severity      Severity  `json:"severity"`
	Value         float64   `json:"value"`
	Threshold     float64   `json:"threshold"`
}

// ParseAlert decodes and validates an alert message. The timestamp is returned in UTC.
func ParseAlert(data []byte) (Alert, error) {
	var a Alert
	if err := decodeFields(data, a.fields()); err != nil {
		return Alert{}, err
	}
	a.TS = a.TS.UTC()
	if err := a.Validate(); err != nil {
		return Alert{}, err
	}
	return a, nil
}

// Validate checks the rules that the Go types alone do not enforce.
func (a Alert) Validate() error {
	switch {
	case a.SchemaVersion != SchemaVersion:
		return fieldErr("schema_version", "unsupported version %d", a.SchemaVersion)
	case !uuidPattern.MatchString(a.AlertID):
		return fieldErr("alert_id", "%q is not a lowercase UUID", a.AlertID)
	case !machineIDPattern.MatchString(a.MachineID):
		return fieldErr("machine_id", "%q does not match %s", a.MachineID, machineIDPattern)
	case a.TS.IsZero():
		return fieldErr("ts", "missing")
	case !slices.Contains(alertTypes, a.Type):
		return fieldErr("type", "%q is not one of %v", a.Type, alertTypes)
	case !slices.Contains(severities, a.Severity):
		return fieldErr("severity", "%q is not one of %v", a.Severity, severities)
	case !isFinite(a.Value):
		return fieldErr("value", "%v is not a finite number", a.Value)
	case !isFinite(a.Threshold):
		return fieldErr("threshold", "%v is not a finite number", a.Threshold)
	}
	return nil
}

func (a *Alert) fields() []field {
	return []field{
		{"schema_version", &a.SchemaVersion},
		{"alert_id", &a.AlertID},
		{"machine_id", &a.MachineID},
		{"ts", &a.TS},
		{"type", &a.Type},
		{"severity", &a.Severity},
		{"value", &a.Value},
		{"threshold", &a.Threshold},
	}
}
