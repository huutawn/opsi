package repositoryanalysis

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var applicationProxySourcePattern = regexp.MustCompile("(?i)[\"']?source[\"']?\\s*:\\s*[\"'](/(?:api|hubs|ws))(?:/[^\"']*)?[\"']")

func inferDependencies(result *Result, files []File, read func(string) ([]byte, error)) {
	if result.Authority == "explicit_config" {
		return
	}
	existing := map[string]bool{}
	for _, dep := range result.Dependencies {
		existing[dep.From+"\x00"+dep.To] = true
	}
	contents := make(map[string][]byte, len(files))
	for _, file := range files {
		data, err := read(file.Path)
		if err == nil {
			contents[file.Path] = data
		}
	}
	proxyConfiguration := map[string]File{}
	proxyPaths := map[string][]string{}
	for _, file := range files {
		text := string(contents[file.Path])
		paths := applicationProxyPaths(text)
		if len(paths) == 0 {
			continue
		}
		for _, app := range result.Applications {
			if underRoot(file.Path, app.Root) {
				proxyConfiguration[app.Key] = file
				proxyPaths[app.Key] = paths
			}
		}
	}
	for _, app := range result.Applications {
		file, ok := proxyConfiguration[app.Key]
		if !ok {
			continue
		}
		for _, target := range result.Applications {
			if target.Key == app.Key {
				continue
			}
			reason := "The web container proxies browser routes to an internal backend URL."
			paths := proxyPaths[app.Key]
			dependency := Dependency{
				From: app.Key, To: target.Key, Protocol: "http", Strategy: "internal_http", Path: paths[0], ProxyPaths: paths, Required: true,
				Injections:   []Injection{{EnvironmentName: "BACKEND_URL", SymbolicSource: "application.internal_url"}},
				Verification: &VerificationContract{Type: "consumer_http", Path: paths[0], ExpectedStatus: 200},
				Confidence:   ConfidenceHigh, Reason: reason,
			}
			for _, path := range paths {
				dependency.Evidence = append(dependency.Evidence, Evidence{Path: file.Path, Kind: "application_proxy", Reason: "The server-side proxy maps " + path + " to BACKEND_URL.", Confidence: ConfidenceHigh})
			}
			replaced := false
			for index := range result.Dependencies {
				current := result.Dependencies[index]
				if current.From == app.Key && current.To == target.Key && current.Protocol == "http" && current.Strategy == "" && hasEvidenceKind(current.Evidence, "compose_dependency") {
					result.Dependencies[index] = dependency
					replaced = true
					break
				}
			}
			if !replaced {
				result.Dependencies = append(result.Dependencies, dependency)
			}
			existing[app.Key+"\x00"+target.Key] = true
			break
		}
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
		data, ok := contents[file.Path]
		if !ok {
			continue
		}
		text := string(data)
		if kafkaIsDisabled(data) {
			markKafkaDisabled(result, file.Path)
		}
		for _, app := range result.Applications {
			if !underRoot(file.Path, app.Root) {
				continue
			}
			inferJWTConfiguration(result, app.Key, file.Path, data)
			detected, ambiguous := detectConnectionEvidence(file.Path, text)
			for _, connection := range detected {
				defaultName := map[string]string{"postgres": "database", "redis": "valkey", "nats": "nats"}[connection.Protocol]
				logicalName := resource(defaultName, connection.Protocol, file.Path, ConfidenceHigh)
				key := app.Key + "\x00" + logicalName
				mapping := Injection{EnvironmentName: connection.EnvironmentName, SymbolicSource: connection.SymbolicSource}
				if !existing[key] {
					result.Dependencies = append(result.Dependencies, Dependency{From: app.Key, To: logicalName, Protocol: connection.Protocol, Required: true, Injections: []Injection{mapping}, Verification: inferredVerification(app, files), Confidence: ConfidenceHigh, Reason: connection.Reason, Evidence: []Evidence{{Path: file.Path, Kind: "connection_dialect", Reason: connection.Reason, Confidence: ConfidenceHigh}}})
					existing[key] = true
				} else {
					addInjection(result, app.Key, logicalName, mapping, file.Path)
				}
			}
			if ambiguous && !hasDialectIssue(result.Issues, app.Key, file.Path) {
				result.Issues = append(result.Issues, Issue{Code: "CONNECTION_DIALECT_REQUIRED", Message: "A connection string is required by " + app.Key + ", but its dialect cannot be determined safely.", Path: file.Path, Resolution: "Select a protocol-specific dialect or a safe connection.template mapping in Review plan.", Blocking: true})
			}
			if isBrowserRouteConsumer(text) && len(browserProxyPaths(text)) > 0 {
				if _, proxied := proxyConfiguration[app.Key]; proxied {
					addApplicationProxyRouteEvidence(result, app.Key, file.Path, text)
					continue
				}
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
		}
	}
}

func hasDialectIssue(issues []Issue, applicationKey, filePath string) bool {
	for _, issue := range issues {
		if issue.Code == "CONNECTION_DIALECT_REQUIRED" && (issue.Path == filePath || strings.Contains(issue.Message, applicationKey)) {
			return true
		}
	}
	return false
}

func inferJWTConfiguration(result *Result, applicationKey, path string, data []byte) {
	text := string(data)
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "jwt") {
		return
	}
	secretEnvironment := ""
	if strings.Contains(text, "Jwt:Key") || strings.Contains(text, "Jwt__Key") {
		secretEnvironment = "Jwt__Key"
	} else if strings.Contains(text, "Jwt:SigningKey") || strings.Contains(text, "Jwt__SigningKey") || strings.Contains(text, "SigningKey") {
		secretEnvironment = "Jwt__SigningKey"
	}
	var document map[string]any
	if json.Unmarshal(data, &document) == nil {
		if jwt, ok := lookupObject(document, "jwt"); ok {
			if _, ok := lookupValue(jwt, "key"); ok {
				secretEnvironment = "Jwt__Key"
			} else if _, ok := lookupValue(jwt, "signingkey"); ok && secretEnvironment == "" {
				secretEnvironment = "Jwt__SigningKey"
			}
			for _, key := range []string{"Issuer", "Audience", "AccessTokenMinutes", "RefreshTokenDays"} {
				value, ok := lookupValue(jwt, strings.ToLower(key))
				if !ok {
					continue
				}
				serialized := ""
				switch typed := value.(type) {
				case string:
					serialized = strings.TrimSpace(typed)
				case float64:
					if typed > 0 && typed == float64(int64(typed)) {
						serialized = fmt.Sprintf("%d", int64(typed))
					}
				}
				if serialized != "" {
					setApplicationEnvironment(result, applicationKey, "Jwt__"+key, serialized)
				}
			}
		}
	}
	if secretEnvironment == "" || hasApplicationSecret(result.Secrets, applicationKey, "jwt-signing-key") {
		return
	}
	reason := "Application configuration requires a JWT signing key."
	result.Secrets = append(result.Secrets, Secret{Name: "jwt-signing-key", ApplicationKey: applicationKey, EnvironmentName: secretEnvironment, Generated: true, SecretRef: "generated://jwt-signing-key", Display: "Generated and securely stored", Confidence: ConfidenceMedium, Reason: reason, Evidence: []Evidence{{Path: path, Kind: "configuration_key", Reason: reason, Confidence: ConfidenceMedium}}})
}

func lookupObject(values map[string]any, key string) (map[string]any, bool) {
	value, ok := lookupValue(values, key)
	object, objectOK := value.(map[string]any)
	return object, ok && objectOK
}

func lookupValue(values map[string]any, key string) (any, bool) {
	for name, value := range values {
		if strings.EqualFold(name, key) {
			return value, true
		}
	}
	return nil, false
}

func setApplicationEnvironment(result *Result, applicationKey, name, value string) {
	for index := range result.Applications {
		if result.Applications[index].Key != applicationKey {
			continue
		}
		if result.Applications[index].Environment == nil {
			result.Applications[index].Environment = map[string]string{}
		}
		result.Applications[index].Environment[name] = value
		return
	}
}

func hasApplicationSecret(secrets []Secret, applicationKey, name string) bool {
	for _, secret := range secrets {
		if secret.ApplicationKey == applicationKey && secret.Name == name {
			return true
		}
	}
	return false
}

func addApplicationProxyRouteEvidence(result *Result, applicationKey, filePath, text string) {
	for index := range result.Dependencies {
		dependency := &result.Dependencies[index]
		if dependency.From != applicationKey || dependency.Strategy != "internal_http" || !hasInjection(dependency.Injections, "BACKEND_URL", "application.internal_url") {
			continue
		}
		for _, route := range browserProxyPaths(text) {
			if !containsPath(dependency.ProxyPaths, route) {
				dependency.ProxyPaths = append(dependency.ProxyPaths, route)
			}
			dependency.Evidence = append(dependency.Evidence, Evidence{Path: filePath, Kind: "browser_route", Reason: "Browser source uses " + route + " through the application proxy.", Confidence: ConfidenceHigh})
		}
	}
}

func browserProxyPaths(text string) []string {
	lower := strings.ToLower(text)
	paths := []string{}
	for _, route := range []string{"/api", "/hubs", "/ws"} {
		if strings.Contains(lower, route) {
			paths = append(paths, route)
		}
	}
	return paths
}

func containsPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}

func hasInjection(injections []Injection, environmentName, symbolicSource string) bool {
	for _, injection := range injections {
		if injection.EnvironmentName == environmentName && injection.SymbolicSource == symbolicSource {
			return true
		}
	}
	return false
}

func hasEvidenceKind(evidence []Evidence, kind string) bool {
	for _, item := range evidence {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func isApplicationProxyConfiguration(text string) bool {
	return len(applicationProxyPaths(text)) > 0
}

func applicationProxyPaths(text string) []string {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "backend_url") || !strings.Contains(lower, "rewrites") || !strings.Contains(lower, "destination") {
		return nil
	}
	present := map[string]bool{}
	for _, match := range applicationProxySourcePattern.FindAllStringSubmatch(text, -1) {
		if len(match) > 1 {
			present[strings.ToLower(match[1])] = true
		}
	}
	paths := make([]string, 0, len(present))
	for _, path := range []string{"/api", "/hubs", "/ws"} {
		if present[path] {
			paths = append(paths, path)
		}
	}
	return paths
}

func isBrowserRouteConsumer(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"fetch(",
		"axios(",
		"axios.",
		"xmlhttprequest",
		"hubconnectionbuilder",
		".withurl(",
		"websocket(",
		"socket.io",
		"destination:",
		"rewrites()",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func markKafkaDisabled(result *Result, evidencePath string) {
	for i := range result.Issues {
		if result.Issues[i].Code == "KAFKA_UNSUPPORTED" {
			result.Issues[i].Blocking = false
			result.Issues[i].Resolution = "Kafka__Enabled=false"
		}
	}
	for i := range result.Resources {
		if result.Resources[i].Type != "kafka" {
			continue
		}
		result.Resources[i].Managed = false
		result.Resources[i].Required = false
		result.Resources[i].Recommendation = "Detected but disabled by Kafka__Enabled=false"
		if !hasEvidence(result.Resources[i].Evidence, evidencePath, "disabled_configuration") {
			result.Resources[i].Evidence = append(result.Resources[i].Evidence, Evidence{Path: evidencePath, Kind: "disabled_configuration", Reason: "Repository configuration explicitly disables Kafka.", Confidence: ConfidenceHigh})
		}
	}
}

func hasEvidence(evidence []Evidence, evidencePath, kind string) bool {
	for _, item := range evidence {
		if item.Path == evidencePath && item.Kind == kind {
			return true
		}
	}
	return false
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
