package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/store"
)

func TestMonitoringRoutesRequireTheirExpectedAuthentication(t *testing.T) {
	server := (&Server{}).Handler()
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/monitoring"},
		{http.MethodPost, "/api/monitoring/targets"},
		{http.MethodGet, "/api/edge/v1/monitoring-targets"},
		{http.MethodPost, "/api/edge/v1/monitoring-results"},
	} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d", test.method, test.path, response.Code)
		}
	}
}

func TestMonitoringAPIConfiguresTargetsAndExposesNodeScore(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("edge-monitor-api", "203.0.113.96")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeCapabilities(node.ID, []string{domain.EdgeCapabilityTCPMonitoring}); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: database}
	create := httptest.NewRequest(http.MethodPost, "/api/monitoring/targets", bytes.NewBufferString(`{"address":"PROBE.example.test:443"}`))
	create = create.WithContext(context.WithValue(create.Context(), adminContextKey{}, "admin"))
	createResponse := httptest.NewRecorder()
	server.createMonitoringTarget(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create target status = %d, body = %s", createResponse.Code, createResponse.Body.String())
	}
	var target domain.MonitoringTarget
	if err := json.Unmarshal(createResponse.Body.Bytes(), &target); err != nil {
		t.Fatal(err)
	}
	result := domain.MonitoringProbeResult{
		TargetID: target.ID, Attempts: 3, SuccessfulAttempts: 2, AverageLatencyMS: 50, Error: "timeout", CheckedAt: time.Now().UTC(),
	}
	reportBody, _ := json.Marshal(monitoringReportRequest{Results: []domain.MonitoringProbeResult{result}})
	report := httptest.NewRequest(http.MethodPost, "/api/edge/v1/monitoring-results", bytes.NewReader(reportBody))
	report = report.WithContext(context.WithValue(report.Context(), edgeContextKey{}, node.ID))
	reportResponse := httptest.NewRecorder()
	server.edgeMonitoringReport(reportResponse, report)
	if reportResponse.Code != http.StatusOK {
		t.Fatalf("report status = %d, body = %s", reportResponse.Code, reportResponse.Body.String())
	}
	overviewResponse := httptest.NewRecorder()
	server.monitoringOverview(overviewResponse, httptest.NewRequest(http.MethodGet, "/api/monitoring", nil))
	if overviewResponse.Code != http.StatusOK {
		t.Fatalf("overview status = %d", overviewResponse.Code)
	}
	var overview monitoringOverviewResponse
	if err := json.Unmarshal(overviewResponse.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if len(overview.Targets) != 1 || overview.Targets[0].Address != "probe.example.test:443" || len(overview.Nodes) != 1 {
		t.Fatalf("overview = %#v", overview)
	}
	monitorNode := overview.Nodes[0]
	if !monitorNode.Capable || monitorNode.Score == nil || *monitorNode.Score >= store.MonitoringHealthyScore || len(monitorNode.Results) != 1 {
		t.Fatalf("monitoring node = %#v", monitorNode)
	}
}

func TestMonitoringReportRejectsFutureTimestamp(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, _ := database.CreateNode("edge-monitor-future", "203.0.113.97")
	target, _ := database.CreateMonitoringTarget("probe.example.test:443")
	input := monitoringReportRequest{Results: []domain.MonitoringProbeResult{{
		TargetID: target.ID, Attempts: 1, SuccessfulAttempts: 1, AverageLatencyMS: 1, CheckedAt: time.Now().Add(10 * time.Minute),
	}}}
	body, _ := json.Marshal(input)
	request := httptest.NewRequest(http.MethodPost, "/api/edge/v1/monitoring-results", bytes.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), edgeContextKey{}, node.ID))
	response := httptest.NewRecorder()
	(&Server{Store: database}).edgeMonitoringReport(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("future report status = %d, body = %s", response.Code, response.Body.String())
	}
}
