package repositoryanalysis

import (
	"encoding/json"
	"strings"
)

func inferDependencies(result *Result, files []File, read func(string) ([]byte, error)) {
	existing := map[string]bool{}
	for _, dep := range result.Dependencies {
		existing[dep.From+"\x00"+dep.To] = true
	}
	resource := func(name, kind, path string, confidence Confidence) string {
		for _, r := range result.Resources {
			if r.Type == kind {
				return r.LogicalName
			}
		}
		reason := "Application configuration references " + kind + "."
		result.Resources = append(result.Resources, Resource{LogicalName: name, Type: kind, Managed: true, Required: true, Recommendation: "Managed " + displayResource(kind), Confidence: confidence, Reason: reason, Evidence: []Evidence{{Path: path, Kind: "source_configuration", Reason: reason, Confidence: confidence}}})
		return name
	}
	for _, file := range files {
		data, err := read(file.Path)
		if err != nil {
			continue
		}
		text := string(data)
		for _, app := range result.Applications {
			if !underRoot(file.Path, app.Root) {
				continue
			}
			if strings.Contains(text, "ConnectionStrings__Database") || strings.Contains(text, "ConnectionStrings:Database") || strings.Contains(text, "ConnectionStrings\":") {
				logicalName := resource("database", "postgres", file.Path, ConfidenceHigh)
				key := app.Key + "\x00" + logicalName
				if !existing[key] {
					reason := "The application reads ConnectionStrings:Database."
					result.Dependencies = append(result.Dependencies, Dependency{From: app.Key, To: logicalName, Protocol: "postgres", Required: true, Injections: []Injection{{EnvironmentName: "ConnectionStrings__Database", SymbolicSource: "resource." + logicalName + ".connection_string"}}, Verification: inferredVerification(app, files), Confidence: ConfidenceHigh, Reason: reason, Evidence: []Evidence{{Path: file.Path, Kind: "configuration_key", Reason: reason, Confidence: ConfidenceHigh}}})
					existing[key] = true
				} else {
					addInjection(result, app.Key, logicalName, Injection{EnvironmentName: "ConnectionStrings__Database", SymbolicSource: "resource." + logicalName + ".connection_string"}, file.Path)
				}
			}
			if strings.Contains(text, "SignalR__Redis__ConnectionString") || strings.Contains(text, "SignalR:Redis:ConnectionString") || strings.Contains(text, "SignalR") && strings.Contains(text, "Redis") && strings.Contains(text, "ConnectionString") {
				logicalName := resource("valkey", "redis", file.Path, ConfidenceHigh)
				key := app.Key + "\x00" + logicalName
				if !existing[key] {
					reason := "The application reads the SignalR Redis connection string."
					result.Dependencies = append(result.Dependencies, Dependency{From: app.Key, To: logicalName, Protocol: "redis", Required: true, Injections: []Injection{{EnvironmentName: "SignalR__Redis__ConnectionString", SymbolicSource: "resource." + logicalName + ".connection_string"}}, Verification: inferredVerification(app, files), Confidence: ConfidenceHigh, Reason: reason, Evidence: []Evidence{{Path: file.Path, Kind: "configuration_key", Reason: reason, Confidence: ConfidenceHigh}}})
					existing[key] = true
				} else {
					addInjection(result, app.Key, logicalName, Injection{EnvironmentName: "SignalR__Redis__ConnectionString", SymbolicSource: "resource." + logicalName + ".connection_string"}, file.Path)
				}
			}
			if strings.Contains(text, "/hubs/notifications") || strings.Contains(text, "/api") {
				for _, target := range result.Applications {
					if target.Key == app.Key {
						continue
					}
					paths := []string{}
					if strings.Contains(text, "/api") {
						paths = append(paths, "/api")
					}
					if strings.Contains(text, "/hubs/notifications") {
						paths = append(paths, "/hubs/notifications")
					}
					for _, p := range paths {
						key := app.Key + "\x00" + target.Key + "\x00" + p
						if !existing[key] {
							reason := "Browser source uses a relative application route."
							result.Dependencies = append(result.Dependencies, Dependency{From: app.Key, To: target.Key, Protocol: "http", Strategy: "same_origin", Path: p, Required: true, Verification: &VerificationContract{Type: "consumer_http", Path: p, ExpectedStatus: 200}, Confidence: ConfidenceMedium, Reason: reason, Evidence: []Evidence{{Path: file.Path, Kind: "same_origin_route", Reason: reason, Confidence: ConfidenceMedium}}})
							result.Bindings = append(result.Bindings, Binding{From: app.Key, To: target.Key, Kind: "browser_http", Path: p, Confidence: ConfidenceMedium, Reason: reason, Evidence: []Evidence{{Path: file.Path, Kind: "same_origin_route", Reason: reason, Confidence: ConfidenceMedium}}})
							existing[key] = true
						}
					}
					break
				}
			}
			if kafkaIsDisabled(data) {
				for i := range result.Issues {
					if result.Issues[i].Code == "KAFKA_UNSUPPORTED" {
						result.Issues[i].Blocking = false
						result.Issues[i].Resolution = "Kafka__Enabled=false"
					}
				}
				for i := range result.Resources {
					if result.Resources[i].Type == "kafka" {
						result.Resources[i].Managed = false
						result.Resources[i].Required = false
						result.Resources[i].Recommendation = "Detected but disabled by Kafka__Enabled=false"
						result.Resources[i].Evidence = append(result.Resources[i].Evidence, Evidence{Path: file.Path, Kind: "disabled_configuration", Reason: "Application configuration explicitly disables Kafka.", Confidence: ConfidenceHigh})
					}
				}
			}
			if strings.Contains(strings.ToLower(text), "jwt") && (strings.Contains(text, "SigningKey") || strings.Contains(text, "JWT")) {
				found := false
				for _, s := range result.Secrets {
					if s.Name == "jwt-signing-key" {
						found = true
					}
				}
				if !found {
					reason := "Application configuration requires a JWT signing key."
					result.Secrets = append(result.Secrets, Secret{Name: "jwt-signing-key", ApplicationKey: app.Key, EnvironmentName: "Jwt__SigningKey", Generated: true, SecretRef: "generated://jwt-signing-key", Display: "Generated and securely stored", Confidence: ConfidenceMedium, Reason: reason, Evidence: []Evidence{{Path: file.Path, Kind: "configuration_key", Reason: reason, Confidence: ConfidenceMedium}}})
				}
			}
		}
	}
}

func kafkaIsDisabled(data []byte) bool {
	lower := strings.ToLower(strings.Join(strings.Fields(string(data)), ""))
	if strings.Contains(lower, "kafka__enabled=false") || strings.Contains(lower, "kafka:enabled=false") || strings.Contains(lower, "kafkaisoptional") {
		return true
	}
	var value any
	if json.Unmarshal(data, &value) == nil {
		return nestedKafkaDisabled(value, false)
	}
	return false
}

func nestedKafkaDisabled(value any, inKafka bool) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			keyLower := strings.ToLower(key)
			if inKafka && keyLower == "enabled" {
				if enabled, ok := nested.(bool); ok && !enabled {
					return true
				}
			}
			if nestedKafkaDisabled(nested, inKafka || keyLower == "kafka") {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if nestedKafkaDisabled(nested, inKafka) {
				return true
			}
		}
	}
	return false
}

func addInjection(result *Result, from, to string, injection Injection, evidencePath string) {
	for i := range result.Dependencies {
		dependency := &result.Dependencies[i]
		if dependency.From != from || dependency.To != to {
			continue
		}
		for _, current := range dependency.Injections {
			if current.EnvironmentName == injection.EnvironmentName {
				return
			}
		}
		dependency.Injections = append(dependency.Injections, injection)
		dependency.Confidence = ConfidenceHigh
		dependency.Evidence = append(dependency.Evidence, Evidence{Path: evidencePath, Kind: "configuration_key", Reason: "Application configuration identifies the exact injection mapping.", Confidence: ConfidenceHigh})
		return
	}
}

func inferredVerification(application Application, files []File) *VerificationContract {
	for _, file := range files {
		if underRoot(file.Path, application.Root) && strings.Contains(strings.ToLower(file.Path), "health") {
			return &VerificationContract{Type: "consumer_http", Path: "/health/ready", ExpectedStatus: 200}
		}
	}
	return nil
}
