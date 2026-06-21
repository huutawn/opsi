package telemetry

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Collector interface {
	Collect(ctx context.Context) ([]MetricRecord, []LogRecord, error)
}

type RuntimeCollector struct {
	ProjectID string
	NodeID    string
	ServiceID string
}

func (c RuntimeCollector) Collect(context.Context) ([]MetricRecord, []LogRecord, error) {
	now := time.Now().UTC()
	metrics := []MetricRecord{
		{ProjectID: c.ProjectID, NodeID: c.NodeID, ServiceID: c.ServiceID, Name: "go.goroutines", Value: float64(runtime.NumGoroutine()), Unit: "count", ObservedAt: now},
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	metrics = append(metrics, MetricRecord{ProjectID: c.ProjectID, NodeID: c.NodeID, ServiceID: c.ServiceID, Name: "process.heap_alloc", Value: float64(mem.HeapAlloc), Unit: "bytes", ObservedAt: now})
	metrics = append(metrics, procMetrics(c.ProjectID, c.NodeID, now)...)
	return metrics, nil, nil
}

func procMetrics(projectID, nodeID string, observedAt time.Time) []MetricRecord {
	var records []MetricRecord
	if load, err := firstLoadAverage(); err == nil {
		records = append(records, MetricRecord{ProjectID: projectID, NodeID: nodeID, Name: "node.load1", Value: load, Unit: "load", ObservedAt: observedAt})
	}
	if memAvailable, err := memAvailableBytes(); err == nil {
		records = append(records, MetricRecord{ProjectID: projectID, NodeID: nodeID, Name: "node.memory_available", Value: float64(memAvailable), Unit: "bytes", ObservedAt: observedAt})
	}
	return records
}

func firstLoadAverage() (float64, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("loadavg is empty")
	}
	return strconv.ParseFloat(fields[0], 64)
}

func memAvailableBytes() (uint64, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("MemAvailable malformed")
		}
		kib, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, err
		}
		return kib * 1024, nil
	}
	return 0, scanner.Err()
}

type Runner struct {
	Store     Store
	Collector Collector
	Interval  time.Duration
}

func (r Runner) Run(ctx context.Context) error {
	interval := r.Interval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if r.Store == nil || r.Collector == nil {
		return nil
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if err := r.collectOnce(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.collectOnce(ctx); err != nil {
				return err
			}
		}
	}
}

func (r Runner) collectOnce(ctx context.Context) error {
	metrics, logs, err := r.Collector.Collect(ctx)
	if err != nil {
		return err
	}
	for _, metric := range metrics {
		if err := r.Store.InsertMetric(ctx, metric); err != nil {
			return err
		}
	}
	for _, logRecord := range logs {
		if err := r.Store.InsertLog(ctx, logRecord); err != nil {
			return err
		}
	}
	return r.Store.Retain(ctx, time.Now().UTC())
}
