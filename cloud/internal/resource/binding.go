package resource

import (
	"context"
	"sort"
	"strconv"
	"strings"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func (s Service) ApplicationEnvironment(ctx context.Context, projectID, environmentID, applicationID string) ([]deploymentv1.EnvironmentVariable, error) {
	environment, _, err := s.ApplicationRuntimeConfiguration(ctx, projectID, environmentID, applicationID)
	return environment, err
}

func (s Service) ApplicationRuntimeConfiguration(ctx context.Context, projectID, environmentID, applicationID string) ([]deploymentv1.EnvironmentVariable, []deploymentv1.SecretReference, error) {
	bindings, err := s.ListBindings(ctx, projectID, environmentID)
	if err != nil {
		return nil, nil, err
	}
	environment := []deploymentv1.EnvironmentVariable{}
	secrets := []deploymentv1.SecretReference{}
	for _, binding := range bindings {
		if binding.Source.ID != applicationID {
			continue
		}
		target, err := s.Get(ctx, projectID, binding.Target.ID)
		if err != nil {
			return nil, nil, err
		}
		prefix := environmentPrefix(binding.LogicalName)
		for _, reference := range runtimeRefs(target) {
			name := prefix + "_" + reference.Name
			if reference.Sensitivity == resourcev1.ValueSecret && reference.SecretRef != nil {
				secrets = append(secrets, deploymentv1.SecretReference{EnvName: name, SecretID: reference.SecretRef.SecretID})
			} else if reference.Sensitivity == resourcev1.ValueNonSecret && reference.Value != "" {
				environment = append(environment, deploymentv1.EnvironmentVariable{Name: name, Value: reference.Value})
			}
		}
	}
	sort.Slice(environment, func(i, j int) bool { return environment[i].Name < environment[j].Name })
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].EnvName < secrets[j].EnvName })
	return environment, secrets, nil
}

func (s Service) ResolveSecretMaterials(ctx context.Context, projectID string, references []deploymentv1.SecretReference) ([]deploymentv1.SecretMaterial, error) {
	if len(references) == 0 {
		return nil, nil
	}
	if s.Credentials == nil {
		return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding credential authority is unavailable")
	}
	resources, err := s.List(ctx, projectID, "")
	if err != nil {
		return nil, err
	}
	byCredential := map[string]resourcev1.Resource{}
	for _, value := range resources {
		if value.Runtime != nil && value.Runtime.Spec.CredentialID != "" {
			byCredential[value.Runtime.Spec.CredentialID] = value
		}
	}
	grouped := map[string]map[string]string{}
	for _, reference := range references {
		target, ok := byCredential[reference.SecretID]
		if !ok || target.Type != resourcev1.TypeRedis {
			return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding secret reference is unavailable")
		}
		credential, err := s.Credentials.Get(ctx, reference.SecretID)
		if err != nil {
			return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding credential is unavailable")
		}
		value := ""
		switch {
		case strings.HasSuffix(reference.EnvName, "_USER"):
			value = credential.Username
		case strings.HasSuffix(reference.EnvName, "_PASSWORD"):
			value = credential.Password
		case strings.HasSuffix(reference.EnvName, "_URL"):
			value = "redis://" + credential.Username + ":" + credential.Password + "@" + target.Runtime.Spec.Connection.Host + ":" + strconv.Itoa(int(target.Runtime.Spec.Connection.Port))
		default:
			return nil, invalid(resourcev1.FailureBindingSecretMaterialization, "resource binding secret name is unsupported")
		}
		if grouped[reference.SecretID] == nil {
			grouped[reference.SecretID] = map[string]string{}
		}
		grouped[reference.SecretID][reference.EnvName] = value
	}
	result := make([]deploymentv1.SecretMaterial, 0, len(grouped))
	for id, values := range grouped {
		result = append(result, deploymentv1.SecretMaterial{SecretID: id, Values: values})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SecretID < result[j].SecretID })
	return result, nil
}

func environmentPrefix(value string) string {
	var out strings.Builder
	underscore := false
	for _, r := range strings.ToUpper(value) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			out.WriteRune(r)
			underscore = false
		} else if !underscore {
			out.WriteByte('_')
			underscore = true
		}
	}
	return strings.Trim(out.String(), "_")
}
