package registry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

func (s PostgresService) GetServiceConfiguration(projectID, serviceID string) (ServiceConfiguration, error) {
	service, err := s.getService(context.Background(), serviceID)
	if err != nil || service.ProjectID != projectID {
		if err == nil {
			err = ErrNotFound
		}
		return ServiceConfiguration{}, err
	}
	return normalizeStoredConfiguration(service.Configuration), nil
}

func (s PostgresService) PreviewServiceConfiguration(projectID, serviceID string, draft ServiceConfigurationDraft) (ServiceConfigurationPreview, error) {
	services, err := s.ListServices(projectID)
	if err != nil {
		return ServiceConfigurationPreview{}, err
	}
	var source ServiceRecord
	for _, service := range services {
		if service.ID == serviceID {
			source = service
			break
		}
	}
	if source.ID == "" {
		return ServiceConfigurationPreview{}, ErrNotFound
	}
	normalized, generated, err := validateServiceConfiguration(source, draft, services)
	if err != nil {
		return ServiceConfigurationPreview{}, err
	}
	current := normalizeStoredConfiguration(source.Configuration)
	return ServiceConfigurationPreview{Configuration: normalized, GeneratedEnvironment: generated, CurrentRevision: current.Revision, CurrentStateHash: current.StateHash, DraftStateHash: serviceConfigurationHash(normalized)}, nil
}

func (s PostgresService) ValidateServiceConfiguration(projectID, serviceID string, draft ServiceConfigurationDraft) (ServiceConfigurationValidation, error) {
	_, err := s.PreviewServiceConfiguration(projectID, serviceID, draft)
	if err == nil {
		return ServiceConfigurationValidation{Valid: true}, nil
	}
	var apiErr APIError
	if errors.As(err, &apiErr) && apiErr.Status == 422 {
		return ServiceConfigurationValidation{Issues: []ServiceConfigurationIssue{{Code: apiErr.Code, Field: apiErr.NextAction, Message: apiErr.Message}}}, nil
	}
	return ServiceConfigurationValidation{}, err
}

func (s PostgresService) DiffServiceConfiguration(projectID, serviceID string, draft ServiceConfigurationDraft) (ServiceConfigurationDiff, error) {
	preview, err := s.PreviewServiceConfiguration(projectID, serviceID, draft)
	if err != nil {
		return ServiceConfigurationDiff{}, err
	}
	current, err := s.GetServiceConfiguration(projectID, serviceID)
	if err != nil {
		return ServiceConfigurationDiff{}, err
	}
	currentPreview, err := s.PreviewServiceConfiguration(projectID, serviceID, current.ServiceConfigurationDraft)
	if err != nil {
		return ServiceConfigurationDiff{}, err
	}
	return configurationDiff(current.ServiceConfigurationDraft, preview.Configuration, currentPreview.GeneratedEnvironment, preview.GeneratedEnvironment), nil
}

func (s PostgresService) ApplyServiceConfiguration(projectID, serviceID, actorUserID, key string, request ServiceConfigurationApplyRequest) (ServiceConfigurationApplyResult, error) {
	ctx := context.Background()
	payload, _ := json.Marshal(request)
	sum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(sum[:])
	operation := "service_configuration_apply:" + serviceID
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ServiceConfigurationApplyResult{}, err
	}
	defer tx.Rollback()
	var storedHash, responseJSON string
	err = tx.QueryRowContext(ctx, `SELECT payload_hash,response_json::text FROM control_mutation_idempotency WHERE project_id=$1 AND operation=$2 AND idempotency_key=$3`, projectID, operation, key).Scan(&storedHash, &responseJSON)
	if err == nil {
		if storedHash != payloadHash {
			return ServiceConfigurationApplyResult{}, APIError{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "idempotency key was already used with a different configuration"}
		}
		var replay ServiceConfigurationApplyResult
		if err := json.Unmarshal([]byte(responseJSON), &replay); err != nil {
			return ServiceConfigurationApplyResult{}, err
		}
		replay.Reused = true
		return replay, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ServiceConfigurationApplyResult{}, err
	}
	source, err := scanService(tx.QueryRowContext(ctx, serviceSelectSQL+` WHERE id=$1 AND project_id=$2 FOR UPDATE`, serviceID, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return ServiceConfigurationApplyResult{}, ErrNotFound
	}
	if err != nil {
		return ServiceConfigurationApplyResult{}, err
	}
	rows, err := tx.QueryContext(ctx, serviceSelectSQL+` WHERE project_id=$1 AND status <> 'deleted' ORDER BY created_at`, projectID)
	if err != nil {
		return ServiceConfigurationApplyResult{}, err
	}
	services := []ServiceRecord{}
	for rows.Next() {
		service, err := scanService(rows)
		if err != nil {
			rows.Close()
			return ServiceConfigurationApplyResult{}, err
		}
		services = append(services, service)
	}
	if err := rows.Close(); err != nil {
		return ServiceConfigurationApplyResult{}, err
	}
	current := normalizeStoredConfiguration(source.Configuration)
	if request.ExpectedRevision != current.Revision || request.ExpectedStateHash != current.StateHash {
		return ServiceConfigurationApplyResult{}, APIError{Status: 409, Code: "SERVICE_CONFIGURATION_STALE", Message: "service configuration revision or state hash is stale"}
	}
	normalized, _, err := validateServiceConfiguration(source, request.Draft, services)
	if err != nil {
		return ServiceConfigurationApplyResult{}, err
	}
	now := s.clock()
	configuration := ServiceConfiguration{ServiceConfigurationDraft: normalized, Revision: current.Revision + 1, StateHash: serviceConfigurationHash(normalized), AppliedBy: actorUserID, AppliedAt: &now}
	configurationJSON, _ := json.Marshal(normalized)
	if _, err := tx.ExecContext(ctx, `UPDATE control_services SET configuration=$1::jsonb,configuration_revision=$2,configuration_state_hash=$3,configuration_applied_by=NULLIF($4,''),configuration_applied_at=$5,updated_at=$5 WHERE id=$6 AND project_id=$7`, string(configurationJSON), configuration.Revision, configuration.StateHash, actorUserID, now, serviceID, projectID); err != nil {
		return ServiceConfigurationApplyResult{}, err
	}
	result := ServiceConfigurationApplyResult{Configuration: configuration}
	response, _ := json.Marshal(result)
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_mutation_idempotency(project_id,operation,idempotency_key,payload_hash,response_json,created_at) VALUES($1,$2,$3,$4,$5::jsonb,$6)`, projectID, operation, key, payloadHash, string(response), now); err != nil {
		return ServiceConfigurationApplyResult{}, err
	}
	return result, tx.Commit()
}
