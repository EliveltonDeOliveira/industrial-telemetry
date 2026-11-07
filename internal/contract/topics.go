package contract

// TelemetrySubscription matches the MQTT telemetry topics of every machine.
const TelemetrySubscription = "fabrica/+/telemetria"

// TelemetryTopic is the MQTT topic a machine publishes its readings to.
func TelemetryTopic(machineID string) string {
	return "fabrica/" + machineID + "/telemetria"
}

// Kafka topics, created by the kafka-init service with 6 partitions each.
const (
	TopicTelemetry = "telemetria.bruta"
	TopicAlerts    = "alertas"
	TopicDLQ       = "telemetria.dlq"
)
