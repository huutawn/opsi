package resource

import (
	"context"
	"sort"
	"strings"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func (s Service) ApplicationEnvironment(ctx context.Context, projectID, environmentID, applicationID string) ([]deploymentv1.EnvironmentVariable, error) {
	bindings, err := s.ListBindings(ctx, projectID, environmentID)
	if err != nil {
		return nil, err
	}
	result := []deploymentv1.EnvironmentVariable{}
	for _, binding := range bindings {
		if binding.Source.ID != applicationID {
			continue
		}
		target, err := s.Get(ctx, projectID, binding.Target.ID)
		if err != nil {
			return nil, err
		}
		prefix := environmentPrefix(binding.LogicalName)
		for _, reference := range runtimeRefs(target) {
			if reference.Sensitivity != resourcev1.ValueNonSecret || reference.Value == "" {
				continue
			}
			result = append(result, deploymentv1.EnvironmentVariable{Name: prefix + "_" + reference.Name, Value: reference.Value})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
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
