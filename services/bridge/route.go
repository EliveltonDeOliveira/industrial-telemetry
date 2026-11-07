package main

import (
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/dlq"
)

const serviceName = "bridge"

// route decides where an MQTT message goes: telemetria.bruta when it follows
// the contract, the DLQ otherwise. The payload is forwarded unchanged, so
// fields the bridge does not know still reach the consumers.
func route(topic string, payload []byte) *kgo.Record {
	t, err := contract.ParseTelemetry(payload)
	if err != nil {
		// The payload may have no valid machine_id, so the key comes from the topic.
		return dlq.Record(serviceName, "mqtt:"+topic, []byte(machineIDFromTopic(topic)), payload, err)
	}
	return &kgo.Record{Topic: contract.TopicTelemetry, Key: []byte(t.MachineID), Value: payload}
}

// machineIDFromTopic returns the {machine_id} level of fabrica/{machine_id}/telemetria.
func machineIDFromTopic(topic string) string {
	if parts := strings.Split(topic, "/"); len(parts) == 3 {
		return parts[1]
	}
	return ""
}
