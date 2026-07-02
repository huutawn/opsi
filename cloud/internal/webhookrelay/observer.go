package webhookrelay

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type Observer struct {
	mu       sync.Mutex
	requests map[string]int64
	errors   map[string]int64
	duration map[string]time.Duration
	statuses map[int]int64
}

func NewObserver() *Observer {
	return &Observer{
		requests: map[string]int64{},
		errors:   map[string]int64{},
		duration: map[string]time.Duration{},
		statuses: map[int]int64{},
	}
}

func (o *Observer) Wrap(next http.Handler) http.Handler {
	if o == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = newID()
			r.Header.Set("X-Request-ID", requestID)
		}
		w.Header().Set("X-Request-ID", requestID)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		o.Record(componentForPath(r.URL.Path), rec.status, time.Since(start))
	})
}

func (o *Observer) Record(component string, status int, duration time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests[component]++
	o.duration[component] += duration
	o.statuses[status]++
	if status >= 400 {
		o.errors[component]++
	}
}

func (o *Observer) Snapshot() ObserverSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	return ObserverSnapshot{
		Requests: cloneStringInt(o.requests),
		Errors:   cloneStringInt(o.errors),
		Duration: cloneStringDuration(o.duration),
		Statuses: cloneIntInt(o.statuses),
	}
}

type ObserverSnapshot struct {
	Requests map[string]int64
	Errors   map[string]int64
	Duration map[string]time.Duration
	Statuses map[int]int64
}

func (s ObserverSnapshot) Prometheus() string {
	var b strings.Builder
	b.WriteString("# TYPE api_requests_total counter\n")
	for _, key := range sortedKeys(s.Requests) {
		fmt.Fprintf(&b, "api_requests_total{component=%q} %d\n", key, s.Requests[key])
	}
	b.WriteString("# TYPE api_errors_total counter\n")
	for _, key := range sortedKeys(s.Errors) {
		fmt.Fprintf(&b, "api_errors_total{component=%q} %d\n", key, s.Errors[key])
	}
	b.WriteString("# TYPE api_request_duration_seconds_sum counter\n")
	for _, key := range sortedKeysDuration(s.Duration) {
		fmt.Fprintf(&b, "api_request_duration_seconds_sum{component=%q} %.6f\n", key, s.Duration[key].Seconds())
	}
	fmt.Fprintf(&b, "rbac_denied_total %d\n", s.Statuses[http.StatusForbidden])
	fmt.Fprintf(&b, "rate_limited_total %d\n", s.Statuses[http.StatusTooManyRequests])
	return b.String()
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("content-type", "text/plain; version=0.0.4")
	if s.observer == nil {
		_, _ = w.Write([]byte(""))
		return
	}
	_, _ = w.Write([]byte(s.observer.Snapshot().Prometheus()))
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func componentForPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "ui"
	}
	switch parts[0] {
	case "api", "v1", "internal", "metrics", "health":
		return parts[0]
	default:
		return "ui"
	}
}

func cloneStringInt(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringDuration(in map[string]time.Duration) map[string]time.Duration {
	out := make(map[string]time.Duration, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneIntInt(in map[int]int64) map[int]int64 {
	out := make(map[int]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sortedKeys(in map[string]int64) []string {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysDuration(in map[string]time.Duration) []string {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
