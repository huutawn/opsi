package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ScenarioConfig defines parameters for a benchmark scenario.
type ScenarioConfig struct {
	Name            string
	WarmupRequests  int
	VULevels        []int
	LevelDuration   time.Duration
	RecoveryBetween time.Duration
	FinalRecovery   time.Duration
	SeedEmail       string
	SeedPassword    string
	EarlyStopP95Ms  float64
	EarlyStop5xxPct float64
	HealthChecker   func() (bool, string)
}

// EarlyStopError records why a run was halted early.
type EarlyStopError struct {
	Scenario string
	VULevel  int
	Reason   string
}

func (e *EarlyStopError) Error() string {
	return fmt.Sprintf("Early stop triggered in scenario %s (VU %d): %s", e.Scenario, e.VULevel, e.Reason)
}

// ScenarioResult holds raw metrics and summaries for a single scenario.
type ScenarioResult struct {
	Scenario        string          `json:"scenario"`
	AllMetrics      []RequestMetric `json:"all_metrics"`
	WarmupMetrics   []RequestMetric `json:"warmup_metrics"`
	LevelMetrics    map[int][]RequestMetric `json:"level_metrics"`
	LevelSummaries  []MetricSummary `json:"level_summaries"`
	EarlyStopReason string          `json:"early_stop_reason,omitempty"`
}

// BenchmarkRunner manages scenario execution and load generation.
type BenchmarkRunner struct {
	probeClient *ProbeClient
	config      ScenarioConfig
}

func NewBenchmarkRunner(probeClient *ProbeClient, config ScenarioConfig) *BenchmarkRunner {
	if config.WarmupRequests <= 0 {
		config.WarmupRequests = 10
	}
	if len(config.VULevels) == 0 {
		config.VULevels = []int{1, 5, 10}
	}
	if config.LevelDuration <= 0 {
		config.LevelDuration = 60 * time.Second
	}
	if config.EarlyStopP95Ms <= 0 {
		config.EarlyStopP95Ms = 10000.0 // 10 seconds
	}
	if config.EarlyStop5xxPct <= 0 {
		config.EarlyStop5xxPct = 2.0 // 2%
	}
	return &BenchmarkRunner{
		probeClient: probeClient,
		config:      config,
	}
}

// RunWarmup executes initial warmup requests sequentially.
func (r *BenchmarkRunner) RunWarmup(ctx context.Context, scenario string) ([]RequestMetric, error) {
	var metrics []RequestMetric
	for i := 0; i < r.config.WarmupRequests; i++ {
		select {
		case <-ctx.Done():
			return metrics, ctx.Err()
		default:
		}

		ms, err := r.executeSingleStep(ctx, scenario, 1, 0, true)
		for idx := range ms {
			ms[idx].IsWarmup = true
			metrics = append(metrics, ms[idx])
		}
		if err != nil {
			// Log but don't fail immediately on single warmup error
		}
		time.Sleep(100 * time.Millisecond)
	}
	return metrics, nil
}

func (r *BenchmarkRunner) executeSingleStep(ctx context.Context, scenario string, vuLevel, vuID int, isWarmup bool) ([]RequestMetric, error) {
	switch scenario {
	case "get_root":
		ms, err := r.probeClient.ProbeRoot(ctx)
		for i := range ms {
			ms[i].VULevel = vuLevel
			ms[i].VUID = vuID
			ms[i].IsWarmup = isWarmup
		}
		return ms, err

	case "post_login":
		m, err := r.probeClient.ProbeLogin(ctx, r.config.SeedEmail, r.config.SeedPassword)
		m.VULevel = vuLevel
		m.VUID = vuID
		m.IsWarmup = isWarmup
		return []RequestMetric{m}, err

	case "post_register":
		m, err := r.probeClient.ProbeRegister(ctx, r.config.SeedPassword)
		m.VULevel = vuLevel
		m.VUID = vuID
		m.IsWarmup = isWarmup
		return []RequestMetric{m}, err

	default:
		return nil, fmt.Errorf("unknown scenario: %s", scenario)
	}
}

// RunVULevel executes a specified number of virtual users for levelDuration.
// Rate cap: max 1 req/VU/sec.
func (r *BenchmarkRunner) RunVULevel(ctx context.Context, scenario string, vuCount int, duration time.Duration) ([]RequestMetric, error) {
	var (
		wg           sync.WaitGroup
		mu           sync.Mutex
		metrics      []RequestMetric
		stopFlag     int32
		stopReason   string
		totalReqs    int64
		http5xxCount int64
	)

	levelCtx, cancelLevel := context.WithTimeout(ctx, duration)
	defer cancelLevel()

	// Periodic health checker
	checkerDone := make(chan struct{})
	go func() {
		defer close(checkerDone)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-levelCtx.Done():
				return
			case <-ticker.C:
				if r.config.HealthChecker != nil {
					ok, reason := r.config.HealthChecker()
					if !ok {
						atomic.StoreInt32(&stopFlag, 1)
						mu.Lock()
						stopReason = "Health check failed: " + reason
						mu.Unlock()
						cancelLevel()
						return
					}
				}
			}
		}
	}()

	startTime := time.Now()
	// Launch VU workers
	for v := 1; v <= vuCount; v++ {
		wg.Add(1)
		go func(vuID int) {
			defer wg.Done()

			executeOnce := func() bool {
				if atomic.LoadInt32(&stopFlag) == 1 {
					return false
				}
				ms, _ := r.executeSingleStep(levelCtx, scenario, vuCount, vuID, false)
				if len(ms) == 0 {
					return true
				}

				total := atomic.AddInt64(&totalReqs, 1)
				var count5xx int64
				for _, m := range ms {
					if m.StatusCode >= 500 {
						count5xx = atomic.AddInt64(&http5xxCount, 1)
					}
				}

				mu.Lock()
				metrics = append(metrics, ms...)

				// Inline early stop checks
				pct5xx := (float64(count5xx) / float64(total)) * 100.0
				if (total >= 5 && pct5xx > r.config.EarlyStop5xxPct) || (total >= 2 && count5xx == total) {
					atomic.StoreInt32(&stopFlag, 1)
					stopReason = fmt.Sprintf("5xx error rate exceeded limit: %.2f%% > %.2f%% (%d/%d)", pct5xx, r.config.EarlyStop5xxPct, count5xx, total)
					cancelLevel()
				} else if len(metrics) >= 10 {
					var recentTotals []float64
					startIdx := len(metrics) - 50
					if startIdx < 0 {
						startIdx = 0
					}
					for _, m := range metrics[startIdx:] {
						if m.Step == "single" || m.Step == "composite_total" {
							recentTotals = append(recentTotals, m.TotalMs)
						}
					}
					if len(recentTotals) >= 10 {
						p95 := CalculatePercentile(recentTotals, 95)
						if p95 > r.config.EarlyStopP95Ms {
							atomic.StoreInt32(&stopFlag, 1)
							stopReason = fmt.Sprintf("p95 latency exceeded limit: %.2fms > %.2fms", p95, r.config.EarlyStopP95Ms)
							cancelLevel()
						}
					}
				}
				mu.Unlock()
				return true
			}

			if !executeOnce() {
				return
			}

			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-levelCtx.Done():
					return
				case <-ticker.C:
					if !executeOnce() {
						return
					}
				}
			}
		}(v)
	}
	wg.Wait()
	<-checkerDone
	actualDuration := time.Since(startTime)
	_ = actualDuration

	if atomic.LoadInt32(&stopFlag) == 1 {
		return metrics, &EarlyStopError{
			Scenario: scenario,
			VULevel:  vuCount,
			Reason:   stopReason,
		}
	}

	return metrics, nil
}

// RunScenario executes warmup + all VU levels for one scenario.
func (r *BenchmarkRunner) RunScenario(ctx context.Context, scenario string) (*ScenarioResult, error) {
	result := &ScenarioResult{
		Scenario:     scenario,
		LevelMetrics: make(map[int][]RequestMetric),
	}

	// 1. Warmup
	warmupMetrics, err := r.RunWarmup(ctx, scenario)
	result.WarmupMetrics = warmupMetrics
	result.AllMetrics = append(result.AllMetrics, warmupMetrics...)
	if err != nil && ctx.Err() != nil {
		return result, err
	}

	// 2. VU Levels: 1 -> 5 -> 10
	for _, vu := range r.config.VULevels {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		levelMetrics, levelErr := r.RunVULevel(ctx, scenario, vu, r.config.LevelDuration)
		result.LevelMetrics[vu] = levelMetrics
		result.AllMetrics = append(result.AllMetrics, levelMetrics...)

		// Compute summaries for this level
		// Filter metrics by step type for reporting
		steps := []string{"single", "composite_total", "redirect_step_1", "redirect_step_2"}
		for _, step := range steps {
			var stepMetrics []RequestMetric
			for _, m := range levelMetrics {
				if m.Step == step {
					stepMetrics = append(stepMetrics, m)
				}
			}
			if len(stepMetrics) > 0 {
				summary := CalculateStats(scenario, vu, step, r.config.LevelDuration, stepMetrics)
				result.LevelSummaries = append(result.LevelSummaries, summary)
			}
		}

		if levelErr != nil {
			if earlyStop, ok := levelErr.(*EarlyStopError); ok {
				result.EarlyStopReason = earlyStop.Reason
				return result, earlyStop
			}
			return result, levelErr
		}
	}

	return result, nil
}
