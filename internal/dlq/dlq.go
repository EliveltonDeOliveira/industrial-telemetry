// Package dlq builds the records sent to the dead letter topic, as defined in
// docs/adr/0004-dead-letter-queue.md and docs/adr/0008-headers-da-dlq.md.
package dlq

import (
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

// Header keys of a dead letter record.
const (
	HeaderError   = "error"   // why the message failed, starting with the field name
	HeaderSource  = "source"  // where it came from, such as "mqtt:fabrica/maq-01/telemetria"
	HeaderService = "service" // the service that gave up on it
)

// Record returns a dead letter record that keeps the original message
// unchanged, so it can be inspected and published again to its source.
func Record(service, source string, key, value []byte, err error) *kgo.Record {
	return &kgo.Record{
		Topic: contract.TopicDLQ,
		Key:   key,
		Value: value,
		Headers: []kgo.RecordHeader{
			{Key: HeaderError, Value: []byte(err.Error())},
			{Key: HeaderSource, Value: []byte(source)},
			{Key: HeaderService, Value: []byte(service)},
		},
	}
}

// Header returns the value of a header of r, or "" if it has none.
func Header(r *kgo.Record, key string) string {
	for _, h := range r.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
