package svcatalog

import (
	"context"
	"fmt"
	"regexp"
)

type Manager struct {
	Store   *Store
	Applier ManifestApplier
}

type CreateManagedRequest struct {
	ProjectID string
	Name      string
	Type      string
	Namespace string
	Overrides map[string]string
}

type DeleteRequest struct {
	ProjectID string
	ID        string
	PurgeData bool
}

func (m Manager) CreateManaged(ctx context.Context, req CreateManagedRequest) (*ManagedService, error) {
	if m.Store == nil {
		return nil, fmt.Errorf("service catalog store is required")
	}
	if m.Applier == nil {
		return nil, fmt.Errorf("manifest applier is required")
	}
	rendered, err := RenderManaged(RenderRequest{
		ProjectID: req.ProjectID,
		Name:      req.Name,
		Type:      req.Type,
		Namespace: req.Namespace,
		Overrides: req.Overrides,
	})
	if err != nil {
		return nil, err
	}
	if err := m.Applier.Apply(ctx, rendered.Service.Namespace, rendered.YAML); err != nil {
		return nil, err
	}
	if err := m.Store.UpsertManagedService(ctx, rendered.Service); err != nil {
		return nil, err
	}
	return &rendered.Service, nil
}

func (m Manager) RegisterExternal(ctx context.Context, req RegisterExternalRequest) (*ManagedService, error) {
	if m.Store == nil {
		return nil, fmt.Errorf("service catalog store is required")
	}
	if m.Applier == nil {
		return nil, fmt.Errorf("manifest applier is required")
	}
	rendered, err := RenderExternal(req)
	if err != nil {
		return nil, err
	}
	if err := m.Applier.Apply(ctx, rendered.Service.Namespace, rendered.YAML); err != nil {
		return nil, err
	}
	if err := m.Store.UpsertManagedService(ctx, rendered.Service); err != nil {
		return nil, err
	}
	return &rendered.Service, nil
}

func (m Manager) Delete(ctx context.Context, req DeleteRequest) error {
	if m.Store == nil {
		return fmt.Errorf("service catalog store is required")
	}
	if req.ProjectID == "" || req.ID == "" {
		return fmt.Errorf("project_id and id are required")
	}
	service, err := m.Store.GetManagedService(ctx, req.ProjectID, req.ID)
	if err != nil {
		return err
	}
	if service == nil {
		return nil
	}
	if deleter, ok := m.Applier.(ResourceDeleter); ok {
		if err := deleter.Delete(ctx, service.Namespace, service.ProjectID, service.ID, req.PurgeData); err != nil {
			return err
		}
	}
	return m.Store.DeleteManagedService(ctx, req.ProjectID, req.ID)
}

func validateRenderRequest(req RenderRequest) error {
	if req.ProjectID == "" {
		return fmt.Errorf("project_id is required")
	}
	if req.Name == "" {
		return fmt.Errorf("service name is required")
	}
	if !safeKubernetesName(req.Name) {
		return fmt.Errorf("service name %q must be Kubernetes-safe", req.Name)
	}
	if req.Namespace != "" && !safeKubernetesName(req.Namespace) {
		return fmt.Errorf("namespace %q must be Kubernetes-safe", req.Namespace)
	}
	if req.Type == "" {
		return fmt.Errorf("service type is required")
	}
	return nil
}

func safeKubernetesName(value string) bool {
	return regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`).MatchString(value) && len(value) <= 63
}
