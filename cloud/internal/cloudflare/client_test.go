package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReconcileCreateUpdateConflictAndDeleteOwnership(t *testing.T) {
	var mu sync.Mutex
	var record *Record
	token := "super-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization header missing")
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dns_records"):
			values := []Record{}
			if record != nil {
				values = append(values, *record)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": values})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dns_records"):
			var value Record
			_ = json.NewDecoder(r.Body).Decode(&value)
			value.ID = "rec-1"
			record = &value
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": value})
		case r.Method == http.MethodPatch:
			var value Record
			_ = json.NewDecoder(r.Body).Decode(&value)
			value.ID = "rec-1"
			record = &value
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": value})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/dns_records/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": record})
		case r.Method == http.MethodDelete:
			record = nil
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]string{"id": "rec-1"}})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	client, err := New(Options{ZoneID: "zone", APIToken: token, Domain: "test.example.com", APIURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	created, err := client.ReconcileARecord(context.Background(), "app.test.example.com", "203.0.113.9", "allocation")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "rec-1" || !created.Proxied || created.TTL != 1 {
		t.Fatalf("created=%+v", created)
	}
	updated, err := client.ReconcileARecord(context.Background(), "app.test.example.com", "203.0.113.10", "allocation")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Content != "203.0.113.10" {
		t.Fatalf("updated=%+v", updated)
	}
	mu.Lock()
	record.Comment = "operator"
	record.Tags = nil
	mu.Unlock()
	if _, err := client.ReconcileARecord(context.Background(), "app.test.example.com", "203.0.113.11", "allocation"); err != ErrDNSConflict {
		t.Fatalf("conflict err=%v", err)
	}
	mu.Lock()
	record.Comment = Marker("allocation")
	mu.Unlock()
	if err := client.DeleteARecord(context.Background(), "rec-1", "app.test.example.com", "allocation"); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteWithoutPersistedRecordIDFindsOnlyOwnedMarker(t *testing.T) {
	var deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dns_records"):
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []Record{{ID: "owned", Type: "A", Name: "app.test.example.com", Comment: Marker("allocation")}}})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dns_records/owned"):
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": Record{ID: "owned", Type: "A", Name: "app.test.example.com", Comment: Marker("allocation")}})
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/dns_records/owned"):
			deleted = true
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]string{"id": "owned"}})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	client, _ := New(Options{ZoneID: "zone", APIToken: "token", Domain: "test.example.com", APIURL: server.URL})
	if err := client.DeleteARecord(context.Background(), "", "app.test.example.com", "allocation"); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("owned record was not deleted")
	}
}

func TestErrorsNeverContainToken(t *testing.T) {
	token := "do-not-leak-this-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"internal"}]}`))
	}))
	defer server.Close()
	client, _ := New(Options{ZoneID: "zone", APIToken: token, Domain: "test.example.com", APIURL: server.URL})
	_, err := client.ReconcileARecord(context.Background(), "app.test.example.com", "203.0.113.9", "a")
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("err=%v", err)
	}
}

func TestRateLimitRetryIsBounded(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1015,"message":"rate limited"}]}`))
			return
		}
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": Record{ID: "record"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []Record{}})
	}))
	defer server.Close()
	client, _ := New(Options{ZoneID: "zone", APIToken: "token", Domain: "test.example.com", APIURL: server.URL})
	if _, err := client.ReconcileARecord(context.Background(), "app.test.example.com", "203.0.113.9", "allocation"); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		// GET is retried once and successful, then POST creates the record.
		t.Fatalf("calls=%d", calls)
	}
}

func TestRecordWriteFallsBackToCommentWhenTagsAreUnavailable(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dns_records") {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []Record{}})
			return
		}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/dns_records") {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		writes++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if writes == 1 {
			if _, ok := body["tags"]; !ok {
				t.Fatal("first write omitted ownership tag")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"code": 9300, "message": "DNS record has 1 tags, exceeding the quota of 0."}}})
			return
		}
		if _, ok := body["tags"]; ok {
			t.Fatal("fallback write retained unsupported tags")
		}
		if body["comment"] != Marker("allocation") {
			t.Fatalf("fallback comment=%v", body["comment"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": Record{ID: "record", Type: "A", Name: "app.test.example.com", Content: "203.0.113.9", Proxied: true, TTL: 1, Comment: Marker("allocation")}})
	}))
	defer server.Close()
	client, err := New(Options{ZoneID: "zone", APIToken: "token", Domain: "test.example.com", APIURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	record, err := client.ReconcileARecord(t.Context(), "app.test.example.com", "203.0.113.9", "allocation")
	if err != nil || record.ID != "record" || writes != 2 {
		t.Fatalf("record=%+v writes=%d err=%v", record, writes, err)
	}
}

func TestRulesPreserveForeignRulesAndPatchOnlyOpsiRefs(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/rulesets") {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []ruleset{{ID: "config", Kind: "zone", Phase: "http_config_settings"}, {ID: "redirect", Kind: "zone", Phase: "http_request_dynamic_redirect"}}})
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config") {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": ruleset{ID: "config", Rules: []rule{{ID: "foreign", Ref: "operator"}, {ID: "ours", Ref: flexibleRuleRef}}}})
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/redirect") {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": ruleset{ID: "redirect", Rules: []rule{{ID: "foreign2", Ref: "operator"}}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{}})
	}))
	defer server.Close()
	client, _ := New(Options{ZoneID: "zone", APIToken: "token", Domain: "test.example.com", APIURL: server.URL})
	if err := client.ReconcileZoneRules(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(methods, "\n")
	if !strings.Contains(joined, "PATCH /client") && !strings.Contains(joined, "PATCH /zones/zone/rulesets/config/rules/ours") {
		t.Fatalf("calls:\n%s", joined)
	}
	if strings.Contains(joined, "rules/foreign") {
		t.Fatalf("foreign rule mutated:\n%s", joined)
	}
}

func TestRulesCreateMissingPhaseEntryPoints(t *testing.T) {
	created := 0
	var expressions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []ruleset{}})
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rulesets") {
			created++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["kind"] != "zone" || body["phase"] == "" {
				t.Errorf("body=%v", body)
			}
			if rules, ok := body["rules"].([]any); ok && len(rules) == 1 {
				if rule, ok := rules[0].(map[string]any); ok {
					if expression, ok := rule["expression"].(string); ok {
						expressions = append(expressions, expression)
					}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": ruleset{ID: "created"}})
			return
		}
		t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	client, _ := New(Options{ZoneID: "zone", APIToken: "token", Domain: "test.example.com", APIURL: server.URL})
	if err := client.ReconcileZoneRules(context.Background()); err != nil {
		t.Fatal(err)
	}
	if created != 2 {
		t.Fatalf("created=%d", created)
	}
	if !slices.Contains(expressions, `not ssl and ends_with(http.host, ".test.example.com")`) {
		t.Fatalf("missing HTTP-only redirect expression: %v", expressions)
	}
}

func TestRulesDisableRemovesOnlyOpsiRules(t *testing.T) {
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/rulesets"):
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []ruleset{{ID: "config", Kind: "zone", Phase: "http_config_settings"}, {ID: "redirect", Kind: "zone", Phase: "http_request_dynamic_redirect"}}})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": ruleset{ID: "config", Rules: []rule{{ID: "ours-config", Ref: flexibleRuleRef}, {ID: "foreign", Ref: "operator"}}}})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/redirect"):
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": ruleset{ID: "redirect", Rules: []rule{{ID: "ours-redirect", Ref: redirectRuleRef}}}})
		case r.Method == http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{}})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	client, _ := New(Options{ZoneID: "zone", APIToken: "token", Domain: "example.com", APIURL: server.URL, DisableFlexibleOrigin: true})
	if err := client.ReconcileZoneRules(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(deleted, "/zones/zone/rulesets/config/rules/ours-config") || !slices.Contains(deleted, "/zones/zone/rulesets/redirect/rules/ours-redirect") || len(deleted) != 2 {
		t.Fatalf("deleted=%v", deleted)
	}
}

func TestRulesReconciliationIsSerialized(t *testing.T) {
	var mu sync.Mutex
	active, maximum := 0, 0
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/rulesets") {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []ruleset{}})
			return
		}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/rulesets") {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		mu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		mu.Unlock()
		started <- struct{}{}
		<-release
		mu.Lock()
		active--
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": ruleset{ID: "created"}})
	}))
	defer server.Close()
	client, _ := New(Options{ZoneID: "zone", APIToken: "token", Domain: "test.example.com", APIURL: server.URL})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := client.ReconcileZoneRules(context.Background()); err != nil {
				t.Errorf("reconcile rules: %v", err)
			}
		}()
	}
	<-started
	select {
	case <-started:
		t.Fatal("concurrent Cloudflare ruleset mutation")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if maximum != 1 {
		t.Fatalf("concurrent mutations=%d", maximum)
	}
}
