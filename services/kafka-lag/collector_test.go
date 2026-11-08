package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeSource struct {
	snap snapshot
	err  error
}

func (f fakeSource) read(context.Context) (snapshot, error) { return f.snap, f.err }

func TestCollect(t *testing.T) {
	src := fakeSource{snap: snapshot{
		groups: []groupLag{
			{group: "telemetry", members: 1, lags: []partitionValue{{"telemetria.bruta", 0, 0}, {"telemetria.bruta", 1, 12}}},
			{group: "maintenance", members: 0}, // stopped, with nothing to read
		},
		ends: []partitionValue{{"alertas", 5, 9}, {"telemetria.bruta", 1, 70321}},
	}}
	want := `
# HELP kafka_consumergroup_lag Messages in the partition after the offset the group committed. A partition without a commit counts whole.
# TYPE kafka_consumergroup_lag gauge
kafka_consumergroup_lag{group="telemetry",partition="0",topic="telemetria.bruta"} 0
kafka_consumergroup_lag{group="telemetry",partition="1",topic="telemetria.bruta"} 12
# HELP kafka_consumergroup_members Members of the group. 0 means no consumer is running, so the lag can only grow.
# TYPE kafka_consumergroup_members gauge
kafka_consumergroup_members{group="maintenance"} 0
kafka_consumergroup_members{group="telemetry"} 1
# HELP kafka_lag_up 1 if the last scrape read the groups and offsets from Kafka, 0 if it failed.
# TYPE kafka_lag_up gauge
kafka_lag_up 1
# HELP kafka_partition_end_offset Offset of the next message written to the partition. It only grows, so rate() of it is messages per second.
# TYPE kafka_partition_end_offset gauge
kafka_partition_end_offset{partition="1",topic="telemetria.bruta"} 70321
kafka_partition_end_offset{partition="5",topic="alertas"} 9
`
	if err := testutil.CollectAndCompare(collector{src: src, timeout: time.Second}, strings.NewReader(want)); err != nil {
		t.Error(err)
	}
}

func TestCollectKafkaDown(t *testing.T) {
	src := fakeSource{err: errors.New("dial tcp: connection refused")}
	want := `
# HELP kafka_lag_up 1 if the last scrape read the groups and offsets from Kafka, 0 if it failed.
# TYPE kafka_lag_up gauge
kafka_lag_up 0
`
	if err := testutil.CollectAndCompare(collector{src: src, timeout: time.Second}, strings.NewReader(want)); err != nil {
		t.Error(err)
	}
}
