package telemetry

import "time"

type MetricRecord struct {
	ProjectID  string    `json:"project_id"`
	NodeID     string    `json:"node_id"`
	ServiceID  string    `json:"service_id,omitempty"`
	PodID      string    `json:"pod_id,omitempty"`
	Name       string    `json:"name"`
	Value      float64   `json:"value"`
	Unit       string    `json:"unit"`
	ObservedAt time.Time `json:"observed_at"`
}

type MetricAggregateRecord struct {
	ProjectID     string    `json:"project_id"`
	NodeID        string    `json:"node_id"`
	ServiceID     string    `json:"service_id,omitempty"`
	PodID         string    `json:"pod_id,omitempty"`
	Name          string    `json:"name"`
	Unit          string    `json:"unit"`
	BucketStart   time.Time `json:"bucket_start"`
	BucketSeconds int64     `json:"bucket_seconds"`
	Count         int64     `json:"count"`
	Avg           float64   `json:"avg"`
	Min           float64   `json:"min"`
	Max           float64   `json:"max"`
}

type LogRecord struct {
	ProjectID   string    `json:"project_id"`
	NodeID      string    `json:"node_id"`
	ServiceID   string    `json:"service_id,omitempty"`
	PodID       string    `json:"pod_id,omitempty"`
	Namespace   string    `json:"namespace"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
	Fingerprint string    `json:"fingerprint"`
	Unread      bool      `json:"unread"`
	ObservedAt  time.Time `json:"observed_at"`
}

type SyncRecord struct {
	Kind            string                 `json:"kind"`
	Metric          *MetricRecord          `json:"metric,omitempty"`
	MetricAggregate *MetricAggregateRecord `json:"metric_aggregate,omitempty"`
	Log             *LogRecord             `json:"log,omitempty"`
	ObservedAt      time.Time              `json:"observed_at"`
}

type Chunk struct {
	ProjectID      string
	Start          time.Time
	End            time.Time
	RecordCount    int
	Compression    string
	ChecksumSHA256 string
	Payload        []byte
	Done           bool
}
