package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MaxSerialLineBytes bounds one line printed by the micro:bit, line ending included.
// The longest line the firmware prints has 173 bytes.
const MaxSerialLineBytes = 256

// CompleteSerialLine turns one line printed by the micro:bit firmware into a telemetry
// message (ADR 0020). The micro:bit has no clock and does not know which machine it is,
// so the gateway sets machine_id and ts, replacing them if the line has them. The other
// fields are kept as they came, unknown ones included, and the bridge validates the result.
func CompleteSerialLine(line []byte, machineID string, ts time.Time) ([]byte, error) {
	if len(line) > MaxSerialLineBytes {
		return nil, fmt.Errorf("line has %d bytes, more than %d", len(line), MaxSerialLineBytes)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return nil, fmt.Errorf("line is not a JSON object: %w", err)
	}
	if fields == nil {
		return nil, errors.New("line is not a JSON object: null")
	}
	at, err := json.Marshal(ts.UTC().Truncate(time.Millisecond))
	if err != nil {
		return nil, fmt.Errorf("ts: %w", err)
	}
	id, _ := json.Marshal(machineID) // a string always encodes
	fields["machine_id"] = id
	fields["ts"] = at
	return json.Marshal(fields)
}
