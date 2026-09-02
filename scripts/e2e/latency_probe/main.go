package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	var (
		baseURL       = flag.String("url", "https://tcip.opsidev.site", "Target public base URL")
		runID         = flag.String("run-id", "run-3deec7c04e7a01a597deb5b0afc7d6b0", "Deployment Run ID")
		mode          = flag.String("mode", "preflight", "Execution mode: preflight | dry-run | live")
		outputDir     = flag.String("output-dir", "", "Directory to save raw metrics and report")
		levelDuration = flag.Duration("duration", 60*time.Second, "Duration per VU level")
		warmupReqs    = flag.Int("warmup", 10, "Number of warmup requests per scenario")
		recoverySec   = flag.Int("recovery", 90, "Recovery seconds between scenarios")
		finalRecSec   = flag.Int("final-recovery", 120, "Final recovery seconds after all tests")
		seedEmail     = flag.String("seed-email", "", "Seed user email for login scenario")
		seedPassword  = flag.String("seed-password", "P@ssword123!", "Seed user password")
		vuLevelsFlag  = flag.String("vu-levels", "1,5,10", "Comma-separated VU levels")
	)
	flag.Parse()

	if *outputDir == "" {
		tmpDir, err := os.MkdirTemp("", "opsi-latency-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating output dir: %v\n", err)
			os.Exit(1)
		}
		*outputDir = tmpDir
	}

	_ = os.MkdirAll(*outputDir, 0700)
	_ = os.Chmod(*outputDir, 0700)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Fprintln(os.Stderr, "\n[INFO] Interrupt signal received, halting gracefully...")
		cancel()
	}()

	probeClient := NewProbeClient(*baseURL, *runID, 30*time.Second)

	fmt.Printf("====================================================\n")
	fmt.Printf(" Opsi End-to-End Latency Benchmark Probe\n")
	fmt.Printf(" Target URL:   %s\n", *baseURL)
	fmt.Printf(" Run ID:       %s\n", *runID)
	fmt.Printf(" Mode:         %s\n", *mode)
	fmt.Printf(" Output Dir:   %s\n", *outputDir)
	fmt.Printf("====================================================\n\n")

	// Preflight Phase
	fmt.Println("[PREFLIGHT] 1/4 Checking Root endpoint and redirects...")
	rootMetrics, err := probeClient.ProbeRoot(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[FATAL] Preflight root probe failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  -> Root Step 1: HTTP %d (Total: %.2fms, TTFB: %.2fms)\n", rootMetrics[0].StatusCode, rootMetrics[0].TotalMs, rootMetrics[0].TTFBMs)
	fmt.Printf("  -> Root Step 2: HTTP %d (Total: %.2fms, TTFB: %.2fms)\n", rootMetrics[1].StatusCode, rootMetrics[1].TotalMs, rootMetrics[1].TTFBMs)

	fmt.Println("[PREFLIGHT] 2/4 Testing Registration contract...")
	regMetric, err := probeClient.ProbeRegister(ctx, *seedPassword)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[FATAL] Preflight register probe failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  -> Register: HTTP %d (Total: %.2fms, TTFB: %.2fms)\n", regMetric.StatusCode, regMetric.TotalMs, regMetric.TTFBMs)

	// Ensure Seed Account exists for Login
	if *seedEmail == "" {
		*seedEmail = fmt.Sprintf("opsi-latency-seed-%s@example.invalid", *runID)
	}
	fmt.Printf("[PREFLIGHT] 3/4 Ensuring Seed account (%s) for login scenario...\n", *seedEmail)
	// Try login; if fails with 400/401, register the seed account
	loginTest, _ := probeClient.ProbeLogin(ctx, *seedEmail, *seedPassword)
	if loginTest.StatusCode != 200 {
		seedCtx, seedCancel := context.WithTimeout(ctx, 10*time.Second)
		defer seedCancel()
		if err := registerSeedAccount(seedCtx, *baseURL, *seedEmail, *seedPassword); err != nil {
			fmt.Printf("  -> Note registering seed account: %v (may already exist)\n", err)
		}
	}

	fmt.Println("[PREFLIGHT] 4/4 Testing Login contract with seed account...")
	loginMetric, err := probeClient.ProbeLogin(ctx, *seedEmail, *seedPassword)
	if err != nil || loginMetric.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "[FATAL] Preflight login probe failed (HTTP %d): %v\n", loginMetric.StatusCode, err)
		os.Exit(1)
	}
	fmt.Printf("  -> Login: HTTP %d (Total: %.2fms, TTFB: %.2fms, Proto: %s)\n", loginMetric.StatusCode, loginMetric.TotalMs, loginMetric.TTFBMs, loginMetric.Proto)
	fmt.Println("\n[PREFLIGHT] All preflight checks passed successfully!")

	if *mode == "preflight" {
		fmt.Println("[INFO] Preflight mode completed.")
		return
	}
	var vuLevels []int
	if *mode == "dry-run" {
		vuLevels = []int{1, 2}
		*levelDuration = 3 * time.Second
		*warmupReqs = 2
		*recoverySec = 2
		*finalRecSec = 2
	} else {
		for _, part := range strings.Split(*vuLevelsFlag, ",") {
			part = strings.TrimSpace(part)
			if v, err := strconv.Atoi(part); err == nil && v > 0 {
				vuLevels = append(vuLevels, v)
			}
		}
		if len(vuLevels) == 0 {
			vuLevels = []int{1, 5, 10}
		}
	}
	config := ScenarioConfig{
		Name:            "opsi_e2e_latency",
		WarmupRequests:  *warmupReqs,
		VULevels:        vuLevels,
		LevelDuration:   *levelDuration,
		RecoveryBetween: time.Duration(*recoverySec) * time.Second,
		FinalRecovery:   time.Duration(*finalRecSec) * time.Second,
		SeedEmail:       *seedEmail,
		SeedPassword:    *seedPassword,
		EarlyStopP95Ms:  10000.0,
		EarlyStop5xxPct: 2.0,
	}

	runner := NewBenchmarkRunner(probeClient, config)

	scenarios := []string{"get_root", "post_login", "post_register"}
	var (
		allMetrics    []RequestMetric
		allSummaries  []MetricSummary
		scenarioRuns  = make(map[string]*ScenarioResult)
	)

	for sIdx, scenario := range scenarios {
		select {
		case <-ctx.Done():
			fmt.Println("[WARN] Benchmark aborted by context cancellation.")
			break
		default:
		}

		fmt.Printf("====================================================\n")
		fmt.Printf(" STARTING SCENARIO (%d/%d): %s\n", sIdx+1, len(scenarios), scenario)
		fmt.Printf(" Levels: %v | Duration/level: %v | Warmup: %d\n", vuLevels, *levelDuration, *warmupReqs)
		fmt.Printf("====================================================\n")

		res, runErr := runner.RunScenario(ctx, scenario)
		scenarioRuns[scenario] = res
		allMetrics = append(allMetrics, res.AllMetrics...)
		allSummaries = append(allSummaries, res.LevelSummaries...)

		if runErr != nil {
			fmt.Fprintf(os.Stderr, "[ERROR] Scenario %s halted: %v\n", scenario, runErr)
		} else {
			fmt.Printf("[INFO] Scenario %s completed successfully.\n", scenario)
		}

		// Print summary table for this scenario
		fmt.Println("\n--- Scenario Summary ---")
		for _, sm := range res.LevelSummaries {
			if sm.Step == "single" || sm.Step == "composite_total" {
				fmt.Printf("VU: %2d | Step: %-15s | Reqs: %4d | RPS: %5.2f | 2xx: %4d | 5xx: %d | Total p50: %7.2fms | p95: %7.2fms | TTFB p50: %7.2fms\n",
					sm.VULevel, sm.Step, sm.TotalRequests, sm.RPS, sm.SuccessCount, sm.HTTP5xxCount, sm.TotalP50, sm.TotalP95, sm.TTFBP50)
			}
		}
		fmt.Println("------------------------")

		// Recovery interval between scenarios
		if sIdx < len(scenarios)-1 && *recoverySec > 0 {
			fmt.Printf("[RECOVERY] Cooling down for %d seconds before next scenario...\n", *recoverySec)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(*recoverySec) * time.Second):
			}
		}
	}

	// Final Recovery
	if *finalRecSec > 0 {
		fmt.Printf("[FINAL RECOVERY] Waiting %d seconds post-benchmark...\n", *finalRecSec)
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(*finalRecSec) * time.Second):
		}
	}

	// Export artifacts
	csvPath := filepath.Join(*outputDir, "raw_metrics.csv")
	csvFile, err := os.OpenFile(csvPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err == nil {
		_ = ExportMetricsCSV(csvFile, allMetrics)
		csvFile.Close()
		fmt.Printf("[EXPORT] Raw metrics CSV: %s\n", csvPath)
	}

	jsonPath := filepath.Join(*outputDir, "raw_metrics.json")
	jsonFile, err := os.OpenFile(jsonPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err == nil {
		_ = ExportMetricsJSON(jsonFile, allMetrics)
		jsonFile.Close()
		fmt.Printf("[EXPORT] Raw metrics JSON: %s\n", jsonPath)
	}

	summaryPath := filepath.Join(*outputDir, "summary_metrics.json")
	sumFile, err := os.OpenFile(summaryPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err == nil {
		_ = ExportSummariesJSON(sumFile, allSummaries)
		sumFile.Close()
		fmt.Printf("[EXPORT] Summaries JSON: %s\n", summaryPath)
	}

	fmt.Println("\n[COMPLETE] Benchmark probe finished successfully.")
}

func registerSeedAccount(ctx context.Context, baseURL, email, password string) error {
	payload := fmt.Sprintf(`{"Email":%q,"Password":%q,"DisplayName":"Seed Latency User"}`, email, password)
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/auth/register", strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
