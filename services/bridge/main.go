// Command bridge moves telemetry from MQTT to Kafka. Valid readings go to
// telemetria.bruta with key machine_id, invalid ones to telemetria.dlq.
//
// Delivery is at least once: an MQTT message is acknowledged only after Kafka
// stores it, and the MQTT session is persistent, so Mosquitto keeps queued and
// unacknowledged messages while the bridge is down and delivers them again.
//
// Configuration:
//
//	MQTT_URL        broker address (tcp://mosquitto:1883)
//	MQTT_CLIENT_ID  identifies the persistent session (bridge)
//	KAFKA_BROKERS   comma separated seed brokers (kafka:9092)
//	HTTP_ADDR       address of the HTTP server (:8080)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/dlq"
)

var (
	received = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bridge_messages_received_total",
		Help: "MQTT messages received.",
	})
	produced = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "bridge_messages_produced_total",
		Help: "Messages stored by Kafka, by topic.",
	}, []string{"topic"})
	produceFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bridge_produce_failures_total",
		Help: "Messages Kafka did not store. They stay unacknowledged and MQTT delivers them again.",
	})
)

type config struct {
	mqttURL      string
	mqttClientID string
	kafkaBrokers []string
	httpAddr     string
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("bridge stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config{
		mqttURL:      getenv("MQTT_URL", "tcp://mosquitto:1883"),
		mqttClientID: getenv("MQTT_CLIENT_ID", "bridge"),
		kafkaBrokers: strings.Split(getenv("KAFKA_BROKERS", "kafka:9092"), ","),
		httpAddr:     getenv("HTTP_ADDR", ":8080"),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	kafka, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.kafkaBrokers...),
		// A missing topic is an infrastructure problem, like a broker that is down:
		// keep retrying instead of failing the records (ADR 0004).
		kgo.UnknownTopicRetries(-1),
	)
	if err != nil {
		return err
	}
	defer kafka.Close()

	client := mqtt.NewClient(mqtt.NewClientOptions().
		AddBroker(cfg.mqttURL).
		SetClientID(cfg.mqttClientID).
		SetCleanSession(false).
		SetAutoAckDisabled(true).
		// Queued messages can arrive before Subscribe completes, so every message
		// goes to the default handler instead of a per-subscription one.
		SetDefaultPublishHandler(forward(kafka)).
		SetConnectRetry(true).
		SetConnectRetryInterval(2 * time.Second).
		SetOnConnectHandler(func(c mqtt.Client) {
			slog.Info("connected to MQTT", "url", cfg.mqttURL)
			token := c.Subscribe(contract.TelemetrySubscription, 1, nil)
			go func() {
				if token.Wait() && token.Error() != nil {
					slog.Error("MQTT subscribe failed", "error", token.Error())
				}
			}()
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) { slog.Warn("MQTT connection lost", "error", err) }))
	client.Connect()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if !client.IsConnectionOpen() {
			http.Error(w, "mqtt: not connected", http.StatusServiceUnavailable)
			return
		}
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := kafka.Ping(pingCtx); err != nil {
			http.Error(w, "kafka: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.Handle("GET /metrics", promhttp.Handler())

	srv := &http.Server{Addr: cfg.httpAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("bridge started", "kafka_brokers", cfg.kafkaBrokers, "http_addr", cfg.httpAddr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		client.Disconnect(250)
		return err
	}

	// Deliver what is buffered while still connected to MQTT, so those messages
	// are acknowledged. Anything left is delivered again on the next start.
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := kafka.Flush(flushCtx); err != nil {
		slog.Warn("kafka flush incomplete", "error", err)
	}
	client.Disconnect(250)
	return nil
}

// forward returns the MQTT handler. Produce is asynchronous, so the handler
// does not block, and Kafka keeps the order of each machine within its partition.
func forward(kafka *kgo.Client) mqtt.MessageHandler {
	return func(_ mqtt.Client, msg mqtt.Message) {
		received.Inc()
		kafka.Produce(context.Background(), route(msg.Topic(), msg.Payload()), func(r *kgo.Record, err error) {
			if err != nil {
				produceFailures.Inc()
				slog.Error("kafka produce failed, message left unacknowledged", "mqtt_topic", msg.Topic(), "error", err)
				return
			}
			produced.WithLabelValues(r.Topic).Inc()
			if r.Topic == contract.TopicDLQ {
				slog.Warn("message sent to DLQ", "mqtt_topic", msg.Topic(), "error", dlq.Header(r, dlq.HeaderError))
			}
			msg.Ack()
		})
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
