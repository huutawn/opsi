package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"time"
)

// MetricSummary holds aggregated statistics for a specific scenario + VU level.
type MetricSummary struct {
	Scenario        string  `json:"scenario"`
	VULevel         int     `json:"vu_level"`
	Step            string  `json:"step"`
	DurationSec     float64 `json:"duration_sec"`
	TotalRequests   int     `json:"total_requests"`
	RPS             float64 `json:"rps"`
	SuccessCount    int     `json:"success_count"`
	HTTP4xxCount    int     `json:"http_4xx_count"`
	HTTP5xxCount    int     `json:"http_5xx_count"`
	ErrorCount      int     `json:"error_count"`
	TimeoutCount    int     `json:"timeout_count"`

	// Total Latency (ms)
	TotalMin float64 `json:"total_min_ms"`
	TotalP50 float64 `json:"total_p50_ms"`
	TotalP95 float64 `json:"total_p95_ms"`
	TotalP99 float64 `json:"total_p99_ms"`
	TotalMax float64 `json:"total_max_ms"`
	TotalAvg float64 `json:"total_avg_ms"`
	TotalStd float64 `json:"total_std_ms"`

	// TTFB Latency (ms)
	TTFBMin float64 `json:"ttfb_min_ms"`
	TTFBP50 float64 `json:"ttfb_p50_ms"`
	TTFBP95 float64 `json:"ttfb_p95_ms"`
	TTFBP99 float64 `json:"ttfb_p99_ms"`
	TTFBMax float64 `json:"ttfb_max_ms"`
	TTFBAvg float64 `json:"ttfb_avg_ms"`

	// Connect & TLS (ms)
	ConnectP50 float64 `json:"connect_p50_ms"`
	ConnectP95 float64 `json:"connect_p95_ms"`
	ConnectAvg float64 `json:"connect_avg_ms"`
	TLSP50     float64 `json:"tls_p50_ms"`
	TLSP95     float64 `json:"tls_p95_ms"`
	TLSAvg     float64 `json:"tls_avg_ms"`
	DNSP50     float64 `json:"dns_p50_ms"`
	DNSAvg     float64 `json:"dns_avg_ms"`
}

// CalculatePercentile computes the p-th percentile (0 <= p <= 100) using linear interpolation.
func CalculatePercentile(values []float64, p float64) float64 {
	n := len(values)
	if n == 0 {
		return 0.0
	}
	if n == 1 || p <= 0 {
		return values[0]
	}
	if p >= 100 {
		return values[n-1]
	}

	rank := (p / 100.0) * float64(n-1)
	lowerIndex := int(math.Floor(rank))
	upperIndex := int(math.Ceil(rank))
	weight := rank - float64(lowerIndex)

	if lowerIndex == upperIndex {
		return values[lowerIndex]
	}
	return values[lowerIndex]*(1.0-weight) + values[upperIndex]*weight
}

// CalculateStats computes statistical aggregates from a slice of metrics.
func CalculateStats(scenario string, vuLevel int, step string, duration time.Duration, metrics []RequestMetric) MetricSummary {
	summary := MetricSummary{
		Scenario:      scenario,
		VULevel:       vuLevel,
		Step:          step,
		DurationSec:   duration.Seconds(),
		TotalRequests: len(metrics),
	}

	if len(metrics) == 0 {
		return summary
	}

	if duration.Seconds() > 0 {
		summary.RPS = float64(len(metrics)) / duration.Seconds()
	}

	var (
		totals   = make([]float64, 0, len(metrics))
		ttfbs    = make([]float64, 0, len(metrics))
		connects = make([]float64, 0, len(metrics))
		tlss     = make([]float64, 0, len(metrics))
		dnss     = make([]float64, 0, len(metrics))
		totalSum float64
		ttfbSum  float64
		connSum  float64
		tlsSum   float64
		dnsSum   float64
	)

	for _, m := range metrics {
		if m.StatusCode >= 200 && m.StatusCode < 400 {
			summary.SuccessCount++
		} else if m.StatusCode >= 400 && m.StatusCode < 500 {
			summary.HTTP4xxCount++
		} else if m.StatusCode >= 500 {
			summary.HTTP5xxCount++
		}

		if m.ErrorClass == "timeout" {
			summary.TimeoutCount++
		} else if m.ErrorClass != "" && m.ErrorClass != "http_4xx" {
			summary.ErrorCount++
		}

		totals = append(totals, m.TotalMs)
		ttfbs = append(ttfbs, m.TTFBMs)
		totalSum += m.TotalMs
		ttfbSum += m.TTFBMs

		if m.ConnectMs > 0 {
			connects = append(connects, m.ConnectMs)
			connSum += m.ConnectMs
		}
		if m.TLSMs > 0 {
			tlss = append(tlss, m.TLSMs)
			tlsSum += m.TLSMs
		}
		if m.DNSMs > 0 {
			dnss = append(dnss, m.DNSMs)
			dnsSum += m.DNSMs
		}
	}

	sort.Float64s(totals)
	sort.Float64s(ttfbs)
	sort.Float64s(connects)
	sort.Float64s(tlss)
	sort.Float64s(dnss)

	summary.TotalMin = totals[0]
	summary.TotalMax = totals[len(totals)-1]
	summary.TotalP50 = CalculatePercentile(totals, 50)
	summary.TotalP95 = CalculatePercentile(totals, 95)
	summary.TotalP99 = CalculatePercentile(totals, 99)
	summary.TotalAvg = totalSum / float64(len(totals))

	// Stddev
	var varianceSum float64
	for _, v := range totals {
		diff := v - summary.TotalAvg
		varianceSum += diff * diff
	}
	summary.TotalStd = math.Sqrt(varianceSum / float64(len(totals)))

	summary.TTFBMin = ttfbs[0]
	summary.TTFBMax = ttfbs[len(ttfbs)-1]
	summary.TTFBP50 = CalculatePercentile(ttfbs, 50)
	summary.TTFBP95 = CalculatePercentile(ttfbs, 95)
	summary.TTFBP99 = CalculatePercentile(ttfbs, 99)
	summary.TTFBAvg = ttfbSum / float64(len(ttfbs))

	if len(connects) > 0 {
		summary.ConnectP50 = CalculatePercentile(connects, 50)
		summary.ConnectP95 = CalculatePercentile(connects, 95)
		summary.ConnectAvg = connSum / float64(len(connects))
	}
	if len(tlss) > 0 {
		summary.TLSP50 = CalculatePercentile(tlss, 50)
		summary.TLSP95 = CalculatePercentile(tlss, 95)
		summary.TLSAvg = tlsSum / float64(len(tlss))
	}
	if len(dnss) > 0 {
		summary.DNSP50 = CalculatePercentile(dnss, 50)
		summary.DNSAvg = dnsSum / float64(len(dnss))
	}

	return summary
}

// ExportMetricsCSV writes all raw metric samples as CSV.
func ExportMetricsCSV(w io.Writer, metrics []RequestMetric) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	header := []string{
		"timestamp", "scenario", "vu_level", "vu_id", "sequence", "step",
		"method", "url", "status_code", "proto",
		"dns_ms", "connect_ms", "tls_ms", "ttfb_ms", "total_ms",
		"is_warmup", "error_class", "error_msg",
	}
	if err := cw.Write(header); err != nil {
		return err
	}

	for _, m := range metrics {
		row := []string{
			m.Timestamp.Format(time.RFC3339Nano),
			m.Scenario,
			strconv.Itoa(m.VULevel),
			strconv.Itoa(m.VUID),
			strconv.FormatUint(m.Sequence, 10),
			m.Step,
			m.Method,
			m.URL,
			strconv.Itoa(m.StatusCode),
			m.Proto,
			fmt.Sprintf("%.3f", m.DNSMs),
			fmt.Sprintf("%.3f", m.ConnectMs),
			fmt.Sprintf("%.3f", m.TLSMs),
			fmt.Sprintf("%.3f", m.TTFBMs),
			fmt.Sprintf("%.3f", m.TotalMs),
			strconv.FormatBool(m.IsWarmup),
			m.ErrorClass,
			m.ErrorMsg,
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	return nil
}

// ExportMetricsJSON writes all raw metric samples as formatted JSON.
func ExportMetricsJSON(w io.Writer, metrics []RequestMetric) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(metrics)
}

// ExportSummariesJSON writes summary statistics as formatted JSON.
func ExportSummariesJSON(w io.Writer, summaries []MetricSummary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(summaries)
}
