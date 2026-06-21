package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/klauspost/compress/zstd"
)

const defaultMaxChunkBytes = 256 * 1024

func BuildChunks(ctx context.Context, projectID string, records []SyncRecord, maxChunkBytes int) ([]Chunk, error) {
	if maxChunkBytes <= 0 {
		maxChunkBytes = defaultMaxChunkBytes
	}
	sortSyncRecords(records)

	var chunks []Chunk
	for start := 0; start < len(records); {
		end := start + 1
		var payload []byte
		for end <= len(records) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			candidate, err := encodeDelta(records[start:end])
			if err != nil {
				return nil, err
			}
			if len(candidate) > maxChunkBytes && end > start+1 {
				break
			}
			payload = candidate
			end++
		}
		compressed, checksum, err := compressZstd(payload)
		if err != nil {
			return nil, err
		}
		window := records[start : end-1]
		chunks = append(chunks, Chunk{
			ProjectID:      projectID,
			Start:          window[0].ObservedAt,
			End:            window[len(window)-1].ObservedAt,
			RecordCount:    len(window),
			Compression:    "zstd",
			ChecksumSHA256: checksum,
			Payload:        compressed,
		})
		start = end - 1
		time.Sleep(1 * time.Millisecond)
	}
	if len(chunks) == 0 {
		chunks = append(chunks, Chunk{ProjectID: projectID, Compression: "zstd", Done: true})
		return chunks, nil
	}
	chunks[len(chunks)-1].Done = true
	return chunks, nil
}

func DecodeChunkPayload(payload []byte) ([]map[string]any, error) {
	decoder, err := zstd.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	decoded, err := decoder.DecodeAll(payload, nil)
	if err != nil {
		return nil, err
	}
	var records []map[string]any
	if err := json.Unmarshal(decoded, &records); err != nil {
		return nil, err
	}
	return records, nil
}

func encodeDelta(records []SyncRecord) ([]byte, error) {
	var previous int64
	encoded := make([]map[string]any, 0, len(records))
	for _, record := range records {
		current := record.ObservedAt.Unix()
		item := map[string]any{
			"kind":        record.Kind,
			"ts_delta_ms": (current - previous) * 1000,
		}
		previous = current
		if record.Metric != nil {
			item["metric"] = record.Metric
		}
		if record.MetricAggregate != nil {
			item["metric_aggregate"] = record.MetricAggregate
		}
		if record.Log != nil {
			item["log"] = record.Log
		}
		encoded = append(encoded, item)
	}
	return json.Marshal(encoded)
}

func compressZstd(payload []byte) ([]byte, string, error) {
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		return nil, "", err
	}
	defer encoder.Close()
	compressed := encoder.EncodeAll(payload, nil)
	sum := sha256.Sum256(compressed)
	return compressed, hex.EncodeToString(sum[:]), nil
}

func sortSyncRecords(records []SyncRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].ObservedAt.Before(records[j].ObservedAt)
	})
}
