package control

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/nginx"
	"cdn-platform/internal/store"
)

type securityCoverageNode struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Status         domain.NodeStatus `json:"status"`
	Capable        bool              `json:"capable"`
	Configured     bool              `json:"configured"`
	DesiredVersion int64             `json:"desired_version"`
	AppliedVersion int64             `json:"applied_version"`
	LastError      string            `json:"last_error,omitempty"`
}

type securityOverviewResponse struct {
	Policies        []domain.SecurityPolicy `json:"policies"`
	Bans            []domain.SecurityBan    `json:"bans"`
	ActiveBanCount  int                     `json:"active_ban_count"`
	Events          []domain.SecurityEvent  `json:"events"`
	Nodes           []securityCoverageNode  `json:"nodes"`
	DeploymentError string                  `json:"deployment_error,omitempty"`
}

type securityPolicyRequest struct {
	Name               string                      `json:"name"`
	Enabled            bool                        `json:"enabled"`
	Pattern            string                      `json:"pattern"`
	Action             domain.SecurityPolicyAction `json:"action"`
	BanDurationSeconds int                         `json:"ban_duration_seconds"`
	Priority           int                         `json:"priority"`
}

func (s *Server) securityOverview(deploymentErr error) (securityOverviewResponse, error) {
	policies, err := s.Store.ListSecurityPolicies()
	if err != nil {
		return securityOverviewResponse{}, err
	}
	bans, err := s.Store.ListActiveSecurityBansLimit(500)
	if err != nil {
		return securityOverviewResponse{}, err
	}
	activeBanCount, err := s.Store.CountActiveSecurityBans()
	if err != nil {
		return securityOverviewResponse{}, err
	}
	events, err := s.Store.ListRecentSecurityEvents(100)
	if err != nil {
		return securityOverviewResponse{}, err
	}
	nodes, err := s.Store.ListNodes()
	if err != nil {
		return securityOverviewResponse{}, err
	}
	coverage := make([]securityCoverageNode, 0, len(nodes))
	for _, node := range nodes {
		desiredVersion, err := s.Store.DesiredVersion(node.ID)
		if err != nil {
			return securityOverviewResponse{}, err
		}
		configured := false
		if nodeState, _, stateErr := s.Store.NodeState(node.ID); stateErr == nil {
			configured = nginx.HasSecurityRevision(nodeState.NginxConfig, policies)
		} else if !errors.Is(stateErr, store.ErrNotFound) {
			return securityOverviewResponse{}, stateErr
		}
		coverage = append(coverage, securityCoverageNode{
			ID: node.ID, Name: node.Name, Status: node.Status,
			Capable:        slices.Contains(node.Capabilities, domain.EdgeCapabilitySecurity),
			Configured:     configured,
			DesiredVersion: desiredVersion, AppliedVersion: node.AppliedVersion, LastError: node.LastError,
		})
	}
	result := securityOverviewResponse{Policies: policies, Bans: bans, ActiveBanCount: activeBanCount, Events: events, Nodes: coverage}
	if deploymentErr != nil {
		result.DeploymentError = deploymentErr.Error()
	}
	return result, nil
}

func (s *Server) getSecurityOverview(response http.ResponseWriter, request *http.Request) {
	result, err := s.securityOverview(nil)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func securityPolicyFromRequest(input securityPolicyRequest) domain.SecurityPolicy {
	return domain.SecurityPolicy{
		Name: input.Name, Enabled: input.Enabled, Pattern: input.Pattern, Action: input.Action,
		BanDurationSeconds: input.BanDurationSeconds, Priority: input.Priority,
	}
}

func (s *Server) createSecurityPolicy(response http.ResponseWriter, request *http.Request) {
	var input securityPolicyRequest
	if !readJSON(response, request, &input) {
		return
	}
	policy, err := s.Store.CreateSecurityPolicy(securityPolicyFromRequest(input))
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	deploymentErr := s.Publisher.PublishAll()
	s.audit(request, adminID(request.Context()), "create", "security_policy", policy.ID, policy.Name)
	result, err := s.securityOverview(deploymentErr)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusCreated, result)
}

func (s *Server) updateSecurityPolicy(response http.ResponseWriter, request *http.Request) {
	var input securityPolicyRequest
	if !readJSON(response, request, &input) {
		return
	}
	policy, err := s.Store.UpdateSecurityPolicy(request.PathValue("id"), securityPolicyFromRequest(input))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeStoreError(response, err)
		} else {
			writeError(response, http.StatusBadRequest, err)
		}
		return
	}
	deploymentErr := s.Publisher.PublishAll()
	s.audit(request, adminID(request.Context()), "update", "security_policy", policy.ID, policy.Name)
	result, err := s.securityOverview(deploymentErr)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) deleteSecurityPolicy(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if err := s.Store.DeleteSecurityPolicy(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeStoreError(response, err)
		} else {
			writeError(response, http.StatusConflict, err)
		}
		return
	}
	deploymentErr := s.Publisher.PublishAll()
	s.audit(request, adminID(request.Context()), "delete", "security_policy", id, "")
	result, err := s.securityOverview(deploymentErr)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) deploySecurityPolicies(response http.ResponseWriter, request *http.Request) {
	if err := s.Publisher.PublishAll(); err != nil {
		writeError(response, http.StatusConflict, err)
		return
	}
	s.audit(request, adminID(request.Context()), "deploy", "security_policy", "all", "rebuilt capable edge states")
	result, err := s.securityOverview(nil)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusAccepted, result)
}

func (s *Server) deleteSecurityBan(response http.ResponseWriter, request *http.Request) {
	ip := strings.TrimSpace(request.PathValue("ip"))
	if err := s.Store.DeleteSecurityBan(ip); err != nil {
		writeStoreError(response, err)
		return
	}
	s.audit(request, adminID(request.Context()), "unban", "security_ban", ip, "")
	result, err := s.securityOverview(nil)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) edgeSecurityEvents(response http.ResponseWriter, request *http.Request) {
	var batch domain.EdgeSecurityEventBatch
	if !readJSON(response, request, &batch) {
		return
	}
	if len(batch.Events) == 0 || len(batch.Events) > 200 {
		writeError(response, http.StatusBadRequest, errors.New("security event batch must contain 1-200 events"))
		return
	}
	nodeID := edgeNodeID(request.Context())
	accepted, err := s.Store.RecordSecurityEvents(nodeID, batch.Events)
	if err != nil {
		var inputError *store.SecurityEventInputError
		if errors.As(err, &inputError) {
			writeJSON(response, http.StatusBadRequest, map[string]any{"error": err.Error(), "invalid_event_index": inputError.Index})
		} else {
			writeError(response, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]int{"accepted": accepted})
}

func (s *Server) edgeSecurityBans(response http.ResponseWriter, request *http.Request) {
	bans, err := s.Store.ListActiveSecurityBans()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	edgeBans := make([]domain.EdgeSecurityBan, 0, len(bans))
	for _, ban := range bans {
		edgeBans = append(edgeBans, domain.EdgeSecurityBan{IP: ban.IP, ExpiresAt: ban.ExpiresAt})
	}
	writeJSON(response, http.StatusOK, domain.EdgeSecurityBanState{Bans: edgeBans, GeneratedAt: time.Now().UTC()})
}
