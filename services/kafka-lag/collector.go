package main

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/twmb/franz-go/pkg/kadm"
)

var (
	upDesc = prometheus.NewDesc("kafka_lag_up",
		"1 if the last scrape read the groups and offsets from Kafka, 0 if it failed.", nil, nil)
	lagDesc = prometheus.NewDesc("kafka_consumergroup_lag",
		"Messages in the partition after the offset the group committed. A partition without a commit counts whole.",
		[]string{"group", "topic", "partition"}, nil)
	membersDesc = prometheus.NewDesc("kafka_consumergroup_members",
		"Members of the group. 0 means no consumer is running, so the lag can only grow.", []string{"group"}, nil)
	endDesc = prometheus.NewDesc("kafka_partition_end_offset",
		"Offset of the next message written to the partition. It only grows, so rate() of it is messages per second.",
		[]string{"topic", "partition"}, nil)
)

// snapshot is what one scrape reads from Kafka.
type snapshot struct {
	groups []groupLag
	ends   []partitionValue // end offset of each partition
}

type groupLag struct {
	group   string
	members int
	lags    []partitionValue
}

type partitionValue struct {
	topic     string
	partition int32
	value     int64
}

// source reads a snapshot. kafkaSource is the real one; tests use a fake.
type source interface {
	read(ctx context.Context) (snapshot, error)
}

// collector reads Kafka at every scrape, so the numbers are as fresh as the
// scrape and nothing runs between scrapes.
type collector struct {
	src     source
	timeout time.Duration
}

func (c collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{upDesc, lagDesc, membersDesc, endDesc} {
		ch <- d
	}
}

func (c collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	snap, err := c.src.read(ctx)
	if err != nil {
		// The scrape still succeeds, so kafka_lag_up = 0 tells Kafka down from this exporter down.
		slog.Error("reading kafka failed", "error", err)
		ch <- prometheus.MustNewConstMetric(upDesc, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(upDesc, prometheus.GaugeValue, 1)
	for _, g := range snap.groups {
		ch <- prometheus.MustNewConstMetric(membersDesc, prometheus.GaugeValue, float64(g.members), g.group)
		for _, l := range g.lags {
			ch <- prometheus.MustNewConstMetric(lagDesc, prometheus.GaugeValue, float64(l.value),
				g.group, l.topic, strconv.Itoa(int(l.partition)))
		}
	}
	for _, e := range snap.ends {
		ch <- prometheus.MustNewConstMetric(endDesc, prometheus.GaugeValue, float64(e.value),
			e.topic, strconv.Itoa(int(e.partition)))
	}
}

// kafkaSource reads every consumer group and every topic, except the internal
// ones such as __consumer_offsets.
type kafkaSource struct {
	adm *kadm.Client
}

func (s kafkaSource) read(ctx context.Context) (snapshot, error) {
	var snap snapshot
	// Without group names, Lag describes every group.
	lags, err := s.adm.Lag(ctx)
	if err != nil {
		return snap, err
	}
	for _, g := range lags.Sorted() {
		if err := g.Error(); err != nil {
			slog.Warn("group lag failed", "group", g.Group, "error", err)
			continue
		}
		gl := groupLag{group: g.Group, members: len(g.Members)}
		for _, l := range g.Lag.Sorted() {
			if l.Err != nil || l.Lag < 0 {
				continue
			}
			gl.lags = append(gl.lags, partitionValue{l.Topic, l.Partition, l.Lag})
		}
		snap.groups = append(snap.groups, gl)
	}
	// Without topic names, ListEndOffsets lists every topic.
	ends, err := s.adm.ListEndOffsets(ctx)
	if err != nil {
		return snap, err
	}
	ends.Each(func(o kadm.ListedOffset) {
		if o.Err == nil && !strings.HasPrefix(o.Topic, "__") {
			snap.ends = append(snap.ends, partitionValue{o.Topic, o.Partition, o.Offset})
		}
	})
	return snap, nil
}
