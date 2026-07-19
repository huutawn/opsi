package webhookrelay

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentpolicy"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/topology"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

func (s *Server) handlePlacementAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	if len(parts) >= 3 && parts[2] == "topology" {
		s.handleTopologyAPI(w, r, projectID, parts, principal)
		return true
	}
	if len(parts) >= 3 && parts[2] == "deployment-policies" {
		s.handleDeploymentPolicyAPI(w, r, projectID, parts, principal)
		return true
	}
	if len(parts) == 3 && parts[2] == "routing-decisions" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return true
		}
		var request deploymentpolicyv1.RoutingRequest
		if !decodePlacementJSON(w, r, &request) {
			return true
		}
		decision, err := s.Policies.Route(r.Context(), projectID, request)
		if err != nil {
			writePlacementFailure(w, r, err, &decision)
			return true
		}
		writeJSON(w, http.StatusOK, decision)
		return true
	}
	return false
}

func (s *Server) handleTopologyAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) {
	if len(parts) == 3 && r.Method == http.MethodGet {
		plan, err := s.Topology.Get(r.Context(), projectID)
		writePlacementResult(w, r, plan, err, http.StatusOK)
		return
	}
	if len(parts) == 4 && parts[3] == "facts" && r.Method == http.MethodGet {
		facts, err := s.Topology.Facts.PlacementFacts(r.Context(), projectID)
		writePlacementResult(w, r, facts, err, http.StatusOK)
		return
	}
	if len(parts) == 4 && (parts[3] == "plan" || parts[3] == "validate" || parts[3] == "diff") && r.Method == http.MethodPost {
		var request struct {
			Draft    topologyv1.Draft `json:"draft"`
			PolicyID string           `json:"policy_id,omitempty"`
		}
		if !decodePlacementJSON(w, r, &request) {
			return
		}
		switch parts[3] {
		case "plan":
			value, err := s.Topology.Preview(r.Context(), projectID, request.Draft)
			writePlacementResult(w, r, value, err, http.StatusOK)
		case "diff":
			value, err := s.Topology.Diff(r.Context(), projectID, request.Draft)
			writePlacementResult(w, r, value, err, http.StatusOK)
		case "validate":
			override, err := s.unknownCapacityOverride(r, projectID, request.PolicyID)
			if err != nil {
				writePlacementFailure(w, r, err, nil)
				return
			}
			value, err := s.Topology.ValidateScoped(r.Context(), projectID, request.Draft, override)
			writePlacementResult(w, r, value, err, http.StatusOK)
		}
		return
	}
	if len(parts) == 4 && parts[3] == "apply" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "topology_plan", projectID, "owner", "admin") {
			return
		}
		var request topologyv1.ApplyRequest
		if !decodePlacementJSON(w, r, &request) {
			return
		}
		override, err := s.unknownCapacityOverride(r, projectID, request.PolicyID)
		if err != nil {
			writePlacementFailure(w, r, err, nil)
			return
		}
		value, err := s.Topology.ApplyScoped(r.Context(), projectID, principal.UserID, r.Header.Get("Idempotency-Key"), request, override)
		if err == nil {
			s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "TOPOLOGY_PLAN_APPLIED", "topology_plan", value.Plan.ID, "success", map[string]any{"revision": value.Plan.Revision, "plan_hash": value.Plan.PlanHash, "reused": value.Reused})
		}
		writePlacementResult(w, r, value, err, http.StatusOK)
		return
	}
	if len(parts) == 5 && parts[3] == "capacities" {
		runtimeID := parts[4]
		if r.Method == http.MethodGet {
			value, err := s.Topology.GetOperatorCapacity(r.Context(), projectID, runtimeID)
			writePlacementResult(w, r, value, err, http.StatusOK)
			return
		}
		if r.Method == http.MethodPost {
			if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "operator_capacity", runtimeID, "owner", "admin") {
				return
			}
			var request topologyv1.OperatorCapacityApplyRequest
			if !decodePlacementJSON(w, r, &request) {
				return
			}
			if request.Draft.RuntimeID != runtimeID {
				writePlacementFailure(w, r, topology.Error{Code: "TOPOLOGY_RUNTIME_MISMATCH", Status: 400, Message: "runtime path and request must match"}, nil)
				return
			}
			value, err := s.Topology.ApplyOperatorCapacity(r.Context(), projectID, principal.UserID, r.Header.Get("Idempotency-Key"), request)
			if err == nil {
				s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "OPERATOR_CAPACITY_APPLIED", "operator_capacity", value.Capacity.ID, "success", map[string]any{"runtime_id": runtimeID, "revision": value.Capacity.Revision, "source": "operator_declared", "reused": value.Reused})
			}
			writePlacementResult(w, r, value, err, http.StatusOK)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) handleDeploymentPolicyAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) {
	if len(parts) == 3 && r.Method == http.MethodGet {
		value, err := s.Policies.List(r.Context(), projectID)
		if value == nil {
			value = []deploymentpolicyv1.Policy{}
		}
		writePlacementResult(w, r, map[string]any{"policies": value}, err, http.StatusOK)
		return
	}
	if len(parts) == 4 && (parts[3] == "preview" || parts[3] == "diff" || parts[3] == "apply") && r.Method == http.MethodPost {
		switch parts[3] {
		case "preview":
			var draft deploymentpolicyv1.Draft
			if !decodePlacementJSON(w, r, &draft) {
				return
			}
			value, err := s.Policies.Preview(r.Context(), projectID, draft)
			writePlacementResult(w, r, value, err, http.StatusOK)
		case "diff":
			var request deploymentpolicyv1.ApplyRequest
			if !decodePlacementJSON(w, r, &request) {
				return
			}
			value, err := s.Policies.Diff(r.Context(), projectID, request.PolicyID, request.Draft)
			writePlacementResult(w, r, value, err, http.StatusOK)
		case "apply":
			if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "deployment_policy", projectID, "owner", "admin") {
				return
			}
			var request deploymentpolicyv1.ApplyRequest
			if !decodePlacementJSON(w, r, &request) {
				return
			}
			value, err := s.Policies.Apply(r.Context(), projectID, principal.UserID, r.Header.Get("Idempotency-Key"), request)
			if err == nil {
				s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "DEPLOYMENT_POLICY_APPLIED", "deployment_policy", value.Policy.ID, "success", map[string]any{"revision": value.Policy.Revision, "policy_hash": value.Policy.PolicyHash, "enabled": value.Policy.Draft.Enabled, "reused": value.Reused})
			}
			writePlacementResult(w, r, value, err, http.StatusOK)
		}
		return
	}
	if len(parts) == 4 && r.Method == http.MethodGet {
		value, err := s.Policies.Get(r.Context(), projectID, parts[3])
		writePlacementResult(w, r, value, err, http.StatusOK)
		return
	}
	if len(parts) == 5 && parts[4] == "disable" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "deployment_policy", parts[3], "owner", "admin") {
			return
		}
		var request deploymentpolicyv1.DisableRequest
		if !decodePlacementJSON(w, r, &request) {
			return
		}
		value, err := s.Policies.Disable(r.Context(), projectID, parts[3], principal.UserID, r.Header.Get("Idempotency-Key"), request)
		if err == nil {
			s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "DEPLOYMENT_POLICY_DISABLED", "deployment_policy", value.Policy.ID, "success", map[string]any{"revision": value.Policy.Revision, "policy_hash": value.Policy.PolicyHash, "reused": value.Reused})
		}
		writePlacementResult(w, r, value, err, http.StatusOK)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) unknownCapacityOverride(r *http.Request, projectID, policyID string) (topology.CapacityOverride, error) {
	if policyID == "" {
		return topology.CapacityOverride{}, nil
	}
	policy, err := s.Policies.Get(r.Context(), projectID, policyID)
	if err != nil {
		return topology.CapacityOverride{}, err
	}
	if !policy.Draft.Enabled {
		return topology.CapacityOverride{}, deploymentpolicy.Error{Code: "DEPLOYMENT_POLICY_DISABLED", Status: 409, Message: "capacity override policy is disabled"}
	}
	return topology.CapacityOverride{
		Allowed:       policy.Draft.AllowUnknownCapacity,
		EnvironmentID: policy.Draft.EnvironmentID,
		ServiceKeys:   append([]string(nil), policy.Draft.ServiceKeys...),
		RuntimeIDs:    append([]string(nil), policy.Draft.AllowedRuntimeIDs...),
	}, nil
}

func decodePlacementJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeRegistryError(w, registry.APIError{Status: 400, Code: "INVALID_JSON", Message: "request body is not valid strict JSON", RequestID: r.Header.Get("X-Request-ID")})
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeRegistryError(w, registry.APIError{Status: 400, Code: "INVALID_JSON", Message: "request body must contain one JSON value", RequestID: r.Header.Get("X-Request-ID")})
		return false
	}
	return true
}
func writePlacementResult(w http.ResponseWriter, r *http.Request, value any, err error, status int) {
	if err != nil {
		writePlacementFailure(w, r, err, nil)
		return
	}
	writeJSON(w, status, value)
}
func writePlacementFailure(w http.ResponseWriter, r *http.Request, err error, decision any) {
	status, code, message := 500, "PLACEMENT_INTERNAL", "placement request failed"
	var te topology.Error
	if errors.As(err, &te) {
		status, code, message = te.Status, te.Code, te.Message
	}
	var pe deploymentpolicy.Error
	if errors.As(err, &pe) {
		status, code, message = pe.Status, pe.Code, pe.Message
	}
	if errors.Is(err, topology.ErrNotFound) || errors.Is(err, deploymentpolicy.ErrNotFound) || errors.Is(err, registry.ErrNotFound) {
		status, code, message = 404, "PLACEMENT_NOT_FOUND", "requested placement resource was not found"
	}
	body := map[string]any{"error_code": code, "message": message, "request_id": r.Header.Get("X-Request-ID")}
	if decision != nil {
		body["decision"] = decision
	}
	writeJSON(w, status, body)
}
