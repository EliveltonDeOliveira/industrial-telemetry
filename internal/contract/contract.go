// Package contract defines the messages exchanged between services and validates them.
//
// The JSON Schemas and examples in contracts/ are the source of truth. The tests of
// this package read them, so the Go code cannot drift from the documented contract.
package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
)

// SchemaVersion is the only message version this code accepts.
const SchemaVersion = 1

// machineIDPattern keeps IDs safe as an MQTT topic level and as a Kafka key.
var machineIDPattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// FieldError tells which field made a message invalid.
// Its text goes to the "error" header of messages sent to the DLQ.
type FieldError struct {
	Field string
	Err   error
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Err.Error() }

func (e *FieldError) Unwrap() error { return e.Err }

func fieldErr(field, format string, args ...any) error {
	return &FieldError{Field: field, Err: fmt.Errorf(format, args...)}
}

// field binds a JSON key to the struct field that receives it.
type field struct {
	name string
	dst  any
}

// decodeFields decodes a JSON object one field at a time, so an error names the field.
// Every field is required and null counts as missing. Unknown fields are ignored,
// so a producer can add a field before the consumers know about it.
func decodeFields(data []byte, fields []field) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("message is not a JSON object: %w", err)
	}
	for _, f := range fields {
		value, ok := raw[f.name]
		if !ok || bytes.Equal(value, []byte("null")) {
			return fieldErr(f.name, "missing")
		}
		if err := json.Unmarshal(value, f.dst); err != nil {
			return &FieldError{Field: f.name, Err: err}
		}
	}
	return nil
}

// inRange also rejects NaN, because every comparison with NaN is false.
func inRange(v, lo, hi float64) bool {
	return v >= lo && v <= hi
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
