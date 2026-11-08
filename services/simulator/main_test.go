package main

import (
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    config
		wantErr string
	}{
		{"defaults", nil,
			config{mqttURL: "tcp://mosquitto:1883", httpAddr: ":8084", machines: 3, prefix: "maq", interval: time.Second}, ""},
		{"load test", map[string]string{"SIM_MACHINES": "500", "SIM_PREFIX": "carga", "SIM_INTERVAL": "500ms"},
			config{mqttURL: "tcp://mosquitto:1883", httpAddr: ":8084", machines: 500, prefix: "carga", interval: 500 * time.Millisecond}, ""},
		{"idle", map[string]string{"SIM_MACHINES": "0", "SIM_PREFIX": "carga"},
			config{mqttURL: "tcp://mosquitto:1883", httpAddr: ":8084", machines: 0, prefix: "carga", interval: time.Second}, ""},
		{"negative machines", map[string]string{"SIM_MACHINES": "-1"}, config{}, "SIM_MACHINES: must be a non-negative integer"},
		{"machines not a number", map[string]string{"SIM_MACHINES": "abc"}, config{}, "SIM_MACHINES: must be a non-negative integer"},
		{"uppercase prefix", map[string]string{"SIM_PREFIX": "Carga"}, config{}, "SIM_PREFIX: must be 1 to 32 lowercase letters, digits or hyphens"},
		{"prefix with slash", map[string]string{"SIM_PREFIX": "a/b"}, config{}, "SIM_PREFIX: must be 1 to 32 lowercase letters, digits or hyphens"},
		{"long prefix", map[string]string{"SIM_PREFIX": "abcdefghijklmnopqrstuvwxyz0123456"}, config{}, "SIM_PREFIX: must be 1 to 32 lowercase letters, digits or hyphens"},
		{"bad interval", map[string]string{"SIM_INTERVAL": "0s"}, config{}, "SIM_INTERVAL: must be a positive duration, such as 1s or 500ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"MQTT_URL", "HTTP_ADDR", "SIM_MACHINES", "SIM_PREFIX", "SIM_INTERVAL"} {
				t.Setenv(k, tt.env[k])
			}
			got, err := loadConfig()
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("config = %+v, want %+v", got, tt.want)
			}
		})
	}
}
