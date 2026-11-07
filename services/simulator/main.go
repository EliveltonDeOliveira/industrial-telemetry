// Command simulator publishes telemetry of simulated machines to MQTT, one
// goroutine per machine, and injects faults on POST /faults.
//
// Configuration:
//
//	MQTT_URL      broker address (tcp://mosquitto:1883)
//	HTTP_ADDR     address of the HTTP server (:8084)
//	SIM_MACHINES  number of machines, named maq-01, maq-02... (3)
//	SIM_INTERVAL  time between readings of each machine (1s)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

var (
	published = promauto.NewCounter(prometheus.CounterOpts{
		Name: "simulator_messages_published_total",
		Help: "Readings acknowledged by the MQTT broker.",
	})
	publishFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "simulator_publish_failures_total",
		Help: "Readings not acknowledged by the MQTT broker: not connected, error or timeout.",
	})
)

type config struct {
	mqttURL  string
	httpAddr string
	machines int
	interval time.Duration
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("simulator stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := mqtt.NewClient(mqtt.NewClientOptions().
		AddBroker(cfg.mqttURL).
		SetClientID(fmt.Sprintf("simulator-%08x", rand.Uint32())).
		SetConnectRetry(true).
		SetConnectRetryInterval(2 * time.Second).
		SetOnConnectHandler(func(mqtt.Client) { slog.Info("connected to MQTT", "url", cfg.mqttURL) }).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) { slog.Warn("MQTT connection lost", "error", err) }))
	// With ConnectRetry the token completes only once connected, so it is not
	// waited on. Readings are counted as failures until the broker is reachable.
	client.Connect()
	defer client.Disconnect(250)
	pub := publisher{client: client, timeout: 5 * time.Second}

	start := time.Now()
	machines := make(map[string]*machine, cfg.machines)
	var wg sync.WaitGroup
	for i := range cfg.machines {
		id := fmt.Sprintf("maq-%02d", i+1)
		m := newMachine(id, profiles[i%len(profiles)], rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())), start)
		machines[id] = m
		wg.Go(func() { simulate(ctx, m, cfg.interval, pub.publish) })
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if !client.IsConnectionOpen() {
			http.Error(w, "mqtt: not connected", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.Handle("POST /faults", faultsHandler(machines, time.Now))

	srv := &http.Server{Addr: cfg.httpAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("simulator started", "machines", cfg.machines, "interval", cfg.interval.String(), "http_addr", cfg.httpAddr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	wg.Wait()
	return nil
}

func loadConfig() (config, error) {
	cfg := config{
		mqttURL:  getenv("MQTT_URL", "tcp://mosquitto:1883"),
		httpAddr: getenv("HTTP_ADDR", ":8084"),
	}
	var err error
	if cfg.machines, err = strconv.Atoi(getenv("SIM_MACHINES", "3")); err != nil || cfg.machines < 1 {
		return config{}, errors.New("SIM_MACHINES: must be a positive integer")
	}
	if cfg.interval, err = time.ParseDuration(getenv("SIM_INTERVAL", "1s")); err != nil || cfg.interval <= 0 {
		return config{}, errors.New("SIM_INTERVAL: must be a positive duration, such as 1s or 500ms")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// simulate publishes a reading of m every interval until ctx is done. A random
// first delay spreads the readings of many machines over the interval.
func simulate(ctx context.Context, m *machine, interval time.Duration, publish func(contract.Telemetry)) {
	select {
	case <-time.After(rand.N(interval)):
	case <-ctx.Done():
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		publish(m.step(time.Now()))
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

type publisher struct {
	client  mqtt.Client
	timeout time.Duration
}

// publish sends a reading with QoS 1. The simulator keeps no buffer: while the
// broker is unreachable, readings are lost, like a machine without a gateway.
func (p publisher) publish(t contract.Telemetry) {
	if !p.client.IsConnectionOpen() {
		publishFailures.Inc()
		return
	}
	payload, err := json.Marshal(t)
	if err == nil {
		token := p.client.Publish(contract.TelemetryTopic(t.MachineID), 1, false, payload)
		if token.WaitTimeout(p.timeout) {
			err = token.Error()
		} else {
			err = fmt.Errorf("no acknowledgement within %s", p.timeout)
		}
	}
	if err != nil {
		publishFailures.Inc()
		slog.Warn("publish failed", "machine_id", t.MachineID, "seq", t.Seq, "error", err)
		return
	}
	published.Inc()
}
