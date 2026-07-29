package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/opsi-dev/opsi/cloud/internal/topology"
)

// PlacementFacts exposes factual registry state without granting topology or
// policy packages write access to registry-owned tables.
func (s *Service) PlacementFacts(_ context.Context, projectID string) (topology.Facts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return topology.Facts{}, ErrNotFound
	}
	result := emptyPlacementFacts(projectID)
	for _, value := range s.envs {
		if value.ProjectID == projectID {
			result.Environments = append(result.Environments, topology.EnvironmentFact{ID: value.ID, ProjectID: value.ProjectID, Name: value.Name, Type: value.Type, Status: value.Status})
		}
	}
	for _, value := range s.runtimes {
		if value.ProjectID == projectID {
			result.Runtimes = append(result.Runtimes, topology.RuntimeFact{ID: value.ID, ProjectID: value.ProjectID, EnvironmentID: value.EnvironmentID, Name: value.Name, Type: value.Type, Status: value.Status})
		}
	}
	for _, value := range s.nodes {
		if value.ProjectID == projectID {
			result.Nodes = append(result.Nodes, topology.NodeFact{ID: value.ID, ProjectID: value.ProjectID, RuntimeID: value.RuntimeID, Status: value.Status, CPUCores: value.CPUCores, MemoryMB: value.MemoryMB, LastSeenAt: value.LastSeenAt})
		}
	}
	for _, value := range s.agents {
		if value.ProjectID == projectID {
			result.Agents = append(result.Agents, topology.AgentFact{ID: value.ID, ProjectID: value.ProjectID, RuntimeID: value.RuntimeID, NodeID: value.NodeID, Status: value.Status, Capabilities: value.Capabilities, LastSeenAt: value.LastSeenAt})
		}
	}
	for _, binding := range s.githubServiceBindings {
		if binding.ProjectID == projectID && binding.Status == "active" {
			result.Services = append(result.Services, topology.ServiceFact{ID: binding.ServiceID, ProjectID: binding.ProjectID, Key: binding.ServiceKey})
		}
	}
	return result, nil
}

func (s PostgresService) PlacementFacts(ctx context.Context, projectID string) (topology.Facts, error) {
	result := emptyPlacementFacts(projectID)
	var exists bool
	if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id=$1)`, projectID).Scan(&exists); err != nil || !exists {
		if err == nil {
			return topology.Facts{}, ErrNotFound
		}
		return topology.Facts{}, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,project_id,name,type,status FROM environments WHERE project_id=$1 ORDER BY id`, projectID)
	if err != nil {
		return topology.Facts{}, err
	}
	for rows.Next() {
		var v topology.EnvironmentFact
		if err = rows.Scan(&v.ID, &v.ProjectID, &v.Name, &v.Type, &v.Status); err != nil {
			rows.Close()
			return topology.Facts{}, err
		}
		result.Environments = append(result.Environments, v)
	}
	if err = rows.Close(); err != nil {
		return topology.Facts{}, err
	}
	rows, err = s.DB.QueryContext(ctx, `SELECT id,project_id,environment_id,name,type,status FROM runtimes WHERE project_id=$1 ORDER BY id`, projectID)
	if err != nil {
		return topology.Facts{}, err
	}
	for rows.Next() {
		var v topology.RuntimeFact
		if err = rows.Scan(&v.ID, &v.ProjectID, &v.EnvironmentID, &v.Name, &v.Type, &v.Status); err != nil {
			rows.Close()
			return topology.Facts{}, err
		}
		result.Runtimes = append(result.Runtimes, v)
	}
	if err = rows.Close(); err != nil {
		return topology.Facts{}, err
	}
	rows, err = s.DB.QueryContext(ctx, `SELECT id,project_id,runtime_id,status,COALESCE(cpu_cores,0),COALESCE(memory_mb,0),last_seen_at FROM nodes WHERE project_id=$1 ORDER BY id`, projectID)
	if err != nil {
		return topology.Facts{}, err
	}
	for rows.Next() {
		var v topology.NodeFact
		var seen sql.NullTime
		if err = rows.Scan(&v.ID, &v.ProjectID, &v.RuntimeID, &v.Status, &v.CPUCores, &v.MemoryMB, &seen); err != nil {
			rows.Close()
			return topology.Facts{}, err
		}
		if seen.Valid {
			v.LastSeenAt = &seen.Time
		}
		result.Nodes = append(result.Nodes, v)
	}
	if err = rows.Close(); err != nil {
		return topology.Facts{}, err
	}
	rows, err = s.DB.QueryContext(ctx, `SELECT id,project_id,runtime_id,node_id,status,capabilities::text,last_seen_at FROM agents WHERE project_id=$1 ORDER BY id`, projectID)
	if err != nil {
		return topology.Facts{}, err
	}
	for rows.Next() {
		var v topology.AgentFact
		var raw string
		var seen sql.NullTime
		if err = rows.Scan(&v.ID, &v.ProjectID, &v.RuntimeID, &v.NodeID, &v.Status, &raw, &seen); err != nil {
			rows.Close()
			return topology.Facts{}, err
		}
		if json.Unmarshal([]byte(raw), &v.Capabilities) != nil {
			rows.Close()
			return topology.Facts{}, errors.New("invalid Agent capabilities")
		}
		if seen.Valid {
			v.LastSeenAt = &seen.Time
		}
		result.Agents = append(result.Agents, v)
	}
	if err = rows.Close(); err != nil {
		return topology.Facts{}, err
	}
	rows, err = s.DB.QueryContext(ctx, `SELECT service_id,project_id,service_key FROM github_service_bindings WHERE project_id=$1 AND status='active' ORDER BY service_key`, projectID)
	if err != nil {
		return topology.Facts{}, err
	}
	for rows.Next() {
		var v topology.ServiceFact
		if err = rows.Scan(&v.ID, &v.ProjectID, &v.Key); err != nil {
			rows.Close()
			return topology.Facts{}, err
		}
		result.Services = append(result.Services, v)
	}
	if err = rows.Close(); err != nil {
		return topology.Facts{}, err
	}
	return result, nil
}

func emptyPlacementFacts(projectID string) topology.Facts {
	return topology.Facts{
		ProjectID:    projectID,
		Environments: []topology.EnvironmentFact{},
		Runtimes:     []topology.RuntimeFact{},
		Nodes:        []topology.NodeFact{},
		Agents:       []topology.AgentFact{},
		Services:     []topology.ServiceFact{},
	}
}
