package deploymentworkflow

import (
	"errors"
	"fmt"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

const (
	maxApplicationEnvironmentVariables = 64
	maxApplicationSecretReferences     = 32
)

type runtimeKey struct {
	name   string
	source string
}

func applicationRuntimeKeys(plan Plan, application repositoryanalysis.Application) []runtimeKey {
	keys := make([]runtimeKey, 0, len(application.Environment))
	for name := range application.Environment {
		keys = append(keys, runtimeKey{name: name, source: "plain environment"})
	}
	for _, secret := range plan.Secrets {
		if secret.ApplicationKey == application.Key {
			keys = append(keys, runtimeKey{name: secret.EnvironmentName, source: "secret"})
		}
	}
	for _, dependency := range plan.Dependencies {
		if dependency.From != application.Key {
			continue
		}
		for _, injection := range dependency.Injections {
			keys = append(keys, runtimeKey{name: injection.EnvironmentName, source: "dependency injection"})
		}
	}
	return keys
}

func applicationHasRuntimeKeys(plan Plan, application repositoryanalysis.Application) bool {
	for _, key := range applicationRuntimeKeys(plan, application) {
		if strings.TrimSpace(key.name) != "" {
			return true
		}
	}
	return false
}

func applicationNoEnvironmentReviews(plan Plan) map[string]bool {
	reviews := make(map[string]bool, len(plan.ApplicationEnvironmentReviews))
	for _, review := range plan.ApplicationEnvironmentReviews {
		if review.NoEnvironmentRequired {
			reviews[review.ApplicationSourceKey] = true
		}
	}
	return reviews
}

func validateApplicationRuntimeConfiguration(plan Plan) error {
	applicationsBySource := make(map[string]repositoryanalysis.Application, len(plan.Applications))
	for _, application := range plan.Applications {
		if _, exists := applicationsBySource[application.SourceKey]; exists {
			return errors.New("deployment plan application source keys must be unique")
		}
		applicationsBySource[application.SourceKey] = application

		if len(application.Environment) > maxApplicationEnvironmentVariables {
			return fmt.Errorf("application %s environment variable count exceeds %d", application.Key, maxApplicationEnvironmentVariables)
		}
		for name, value := range application.Environment {
			if !deploymentv1.IsValidEnvironmentName(name) {
				return fmt.Errorf("application %s environment name %q is invalid", application.Key, name)
			}
			if deploymentv1.IsSecretLikeEnvironmentName(name) {
				return fmt.Errorf("application %s environment name %q is secret-like; use a secret reference", application.Key, name)
			}
			if len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
				return fmt.Errorf("application %s environment value for %q exceeds 4096 bytes or contains a forbidden control character", application.Key, name)
			}
		}

		secretCount := 0
		secretNames := make(map[string]bool)
		nonSecretCount := len(application.Environment)
		for _, secret := range plan.Secrets {
			if secret.ApplicationKey != application.Key {
				continue
			}
			if resourcev1.ValidateWorkloadSecretLogicalName(secret.Name) != nil {
				return fmt.Errorf("application %s workload secret logical name is invalid", application.Key)
			}
			if secretNames[secret.Name] {
				return fmt.Errorf("application %s contains duplicate workload secret logical name %q", application.Key, secret.Name)
			}
			secretNames[secret.Name] = true
		}
		seen := make(map[string]string)
		for _, key := range applicationRuntimeKeys(plan, application) {
			if !deploymentv1.IsValidEnvironmentName(key.name) {
				return fmt.Errorf("application %s runtime environment name %q is invalid", application.Key, key.name)
			}
			if previous, exists := seen[key.name]; exists {
				return fmt.Errorf("application %s has duplicate runtime key %q between %s and %s", application.Key, key.name, previous, key.source)
			}
			seen[key.name] = key.source
			if key.source == "secret" {
				secretCount++
			} else if key.source == "dependency injection" {
				nonSecretCount++
			}
		}
		if secretCount > maxApplicationSecretReferences {
			return fmt.Errorf("application %s secret reference count exceeds %d", application.Key, maxApplicationSecretReferences)
		}
		if nonSecretCount > maxApplicationEnvironmentVariables {
			return fmt.Errorf("application %s effective environment variable count exceeds %d", application.Key, maxApplicationEnvironmentVariables)
		}
	}

	seenReviews := make(map[string]bool, len(plan.ApplicationEnvironmentReviews))
	for _, review := range plan.ApplicationEnvironmentReviews {
		application, exists := applicationsBySource[review.ApplicationSourceKey]
		if !exists {
			return errors.New("deployment plan application environment review references an unknown application")
		}
		if seenReviews[review.ApplicationSourceKey] {
			return errors.New("deployment plan contains duplicate application environment reviews")
		}
		seenReviews[review.ApplicationSourceKey] = true
		if !review.NoEnvironmentRequired {
			return errors.New("deployment plan application environment review confirmation is invalid")
		}
		if applicationHasRuntimeKeys(plan, application) {
			return fmt.Errorf("application %s has runtime environment keys and cannot declare that none are required", application.Key)
		}
	}

	for _, application := range plan.Applications {
		if !applicationHasRuntimeKeys(plan, application) && !seenReviews[application.SourceKey] {
			return fmt.Errorf("application %s requires runtime environment configuration or an explicit no-environment confirmation", application.Key)
		}
	}
	return nil
}
