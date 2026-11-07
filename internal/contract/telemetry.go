package contract

import (
	"slices"
	"time"
)

// State is the machine operating state.
type State string

const (
	StateRunning State = "running"
	StateStopped State = "stopped"
)

var states = []State{StateRunning, StateStopped}

// Plausible sensor ranges. A reading outside them is a sensor fault, not a measurement.
const (
	MinTemperatureC = -40.0 // industrial electronics operating range
	MaxTemperatureC = 125.0
	MaxVibrationRMS = 16.0 // full scale of the micro:bit accelerometer, in g
)

// Telemetry is one machine reading.
// Seq and the counters restart when BootID changes, so only compare them within the same BootID.
type Telemetry struct {
	SchemaVersion int       `json:"schema_version"`
	MachineID     string    `json:"machine_id"`
	BootID        uint32    `json:"boot_id"`
	Seq           uint64    `json:"seq"`
	TS            time.Time `json:"ts"`
	State         State     `json:"state"`
	GoodTotal     uint64    `json:"good_total"`
	ScrapTotal    uint64    `json:"scrap_total"`
	TemperatureC  float64   `json:"temperature_c"`
	VibrationRMS  float64   `json:"vibration_rms"`
}

// ParseTelemetry decodes and validates a telemetry message. The timestamp is returned in UTC.
func ParseTelemetry(data []byte) (Telemetry, error) {
	var t Telemetry
	if err := decodeFields(data, t.fields()); err != nil {
		return Telemetry{}, err
	}
	t.TS = t.TS.UTC()
	if err := t.Validate(); err != nil {
		return Telemetry{}, err
	}
	return t, nil
}

// Validate checks the rules that the Go types alone do not enforce.
func (t Telemetry) Validate() error {
	switch {
	case t.SchemaVersion != SchemaVersion:
		return fieldErr("schema_version", "unsupported version %d", t.SchemaVersion)
	case !machineIDPattern.MatchString(t.MachineID):
		return fieldErr("machine_id", "%q does not match %s", t.MachineID, machineIDPattern)
	case t.TS.IsZero():
		return fieldErr("ts", "missing")
	case !slices.Contains(states, t.State):
		return fieldErr("state", "%q is not one of %v", t.State, states)
	case !inRange(t.TemperatureC, MinTemperatureC, MaxTemperatureC):
		return fieldErr("temperature_c", "%v is outside [%v, %v]", t.TemperatureC, MinTemperatureC, MaxTemperatureC)
	case !inRange(t.VibrationRMS, 0, MaxVibrationRMS):
		return fieldErr("vibration_rms", "%v is outside [0, %v]", t.VibrationRMS, MaxVibrationRMS)
	}
	return nil
}

func (t *Telemetry) fields() []field {
	return []field{
		{"schema_version", &t.SchemaVersion},
		{"machine_id", &t.MachineID},
		{"boot_id", &t.BootID},
		{"seq", &t.Seq},
		{"ts", &t.TS},
		{"state", &t.State},
		{"good_total", &t.GoodTotal},
		{"scrap_total", &t.ScrapTotal},
		{"temperature_c", &t.TemperatureC},
		{"vibration_rms", &t.VibrationRMS},
	}
}
