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
	"cdn-platform/internal/logstore"
	"cdn-platform/internal/store"
)

func TestMonitoringRoutesRequireTheirExpectedAuthentication(t *testing.T) {
	server := (&Server{}).Handler()
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/monitoring"},
		{http.MethodGet, "/api/monitoring/nodes/node-1/history"},
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
	create := httptest.NewRequest(http.MethodPost, "/api/monitoring/targets", bytes.NewBufferString(`{"name":"主 API","address":"PROBE.example.test:443"}`))
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
	if len(overview.Targets) != 1 || overview.Targets[0].Name != "主 API" || overview.Targets[0].Address != "probe.example.test:443" || len(overview.Nodes) != 1 {
		t.Fatalf("overview = %#v", overview)
	}
	monitorNode := overview.Nodes[0]
	if !monitorNode.Capable || monitorNode.Score == nil || *monitorNode.Score >= store.MonitoringHealthyScore || len(monitorNode.Results) != 1 || monitorNode.Results[0].TargetName != "主 API" {
		t.Fatalf("monitoring node = %#v", monitorNode)
	}
}

type recordingMonitoringHistory struct {
	query   logstore.MonitoringHistoryQuery
	buckets []logstore.MonitoringHistoryBucket
	err     error
}

func (history *recordingMonitoringHistory) MonitoringHistory(_ context.Context, query logstore.MonitoringHistoryQuery) ([]logstore.MonitoringHistoryBucket, error) {
	history.query = query
	return history.buckets, history.err
}

type recordingMonitoringWriter struct {
	samples []logstore.MonitoringSample
	accept  bool
}

func (writer *recordingMonitoringWriter) EnqueueMonitoring(samples []logstore.MonitoringSample) bool {
	writer.samples = append([]logstore.MonitoringSample(nil), samples...)
	return writer.accept
}

func TestMonitoringReportEnqueuesNamedHistorySamples(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, _ := database.CreateNode("香港边缘", "203.0.113.98")
	target, _ := database.CreateMonitoringTarget("主 API", "api.example.test:443")
	checkedAt := time.Now().UTC().Truncate(time.Millisecond)
	input := monitoringReportRequest{Results: []domain.MonitoringProbeResult{{
		TargetID: target.ID, Attempts: 3, SuccessfulAttempts: 2, AverageLatencyMS: 18.5, Error: "timeout", CheckedAt: checkedAt,
	}}}
	body, _ := json.Marshal(input)
	request := httptest.NewRequest(http.MethodPost, "/api/edge/v1/monitoring-results", bytes.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), edgeContextKey{}, node.ID))
	response := httptest.NewRecorder()
	writer := &recordingMonitoringWriter{accept: true}
	(&Server{Store: database, MonitoringWriter: writer}).edgeMonitoringReport(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("report status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(writer.samples) != 1 {
		t.Fatalf("history samples = %#v", writer.samples)
	}
	sample := writer.samples[0]
	if sample.NodeID != node.ID || sample.NodeName != node.Name || sample.TargetID != target.ID || sample.TargetName != target.Name || sample.TargetAddress != target.Address || !sample.CheckedAt.Equal(checkedAt) {
		t.Fatalf("history sample = %#v", sample)
	}
}

func TestMonitoringNodeHistoryUsesPresetAndCurrentTargetName(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, _ := database.CreateNode("新加坡边缘", "203.0.113.99")
	target, _ := database.CreateMonitoringTarget("当前名称", "api.example.test:443")
	latency := 22.5
	history := &recordingMonitoringHistory{buckets: []logstore.MonitoringHistoryBucket{{
		Time: time.Now().UTC().Add(-time.Minute), NodeID: node.ID, NodeName: node.Name,
		TargetID: target.ID, TargetName: "旧名称", TargetAddress: "old.example.test:443",
		Attempts: 6, SuccessfulAttempts: 5, AverageLatencyMS: &latency, FailedRounds: 1,
	}}}
	request := httptest.NewRequest(http.MethodGet, "/api/monitoring/nodes/"+node.ID+"/history?range=6h", nil)
	request.SetPathValue("id", node.ID)
	response := httptest.NewRecorder()
	(&Server{Store: database, MonitoringHistory: history}).monitoringNodeHistory(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("history status = %d, body = %s", response.Code, response.Body.String())
	}
	var result monitoringHistoryResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Range != "6h" || result.BucketSeconds != 120 || len(result.Series) != 1 {
		t.Fatalf("history response = %#v", result)
	}
	if elapsed := history.query.To.Sub(history.query.From); elapsed < 6*time.Hour-time.Second || elapsed > 6*time.Hour+time.Second || history.query.Bucket != 2*time.Minute || history.query.NodeID != node.ID {
		t.Fatalf("history query = %#v", history.query)
	}
	series := result.Series[0]
	if series.Name != "当前名称" || series.Address != target.Address || len(series.Points) != 1 || series.Points[0].SuccessRate != 100*5.0/6.0 {
		t.Fatalf("history series = %#v", series)
	}
}

func TestMonitoringNodeHistoryValidatesRangeAndDegradesWithoutClickHouse(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, _ := database.CreateNode("东京边缘", "203.0.113.100")
	server := &Server{Store: database}

	invalid := httptest.NewRequest(http.MethodGet, "/api/monitoring/nodes/"+node.ID+"/history?range=30d", nil)
	invalid.SetPathValue("id", node.ID)
	invalidResponse := httptest.NewRecorder()
	server.monitoringNodeHistory(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid range status = %d", invalidResponse.Code)
	}

	unavailable := httptest.NewRequest(http.MethodGet, "/api/monitoring/nodes/"+node.ID+"/history?range=1h", nil)
	unavailable.SetPathValue("id", node.ID)
	unavailableResponse := httptest.NewRecorder()
	server.monitoringNodeHistory(unavailableResponse, unavailable)
	var result monitoringHistoryResponse
	if err := json.Unmarshal(unavailableResponse.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if unavailableResponse.Code != http.StatusOK || result.Available || result.Range != "1h" || result.UnavailableReason == "" {
		t.Fatalf("unavailable history response = %#v", result)
	}
}

func TestMonitoringReportRejectsFutureTimestamp(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, _ := database.CreateNode("edge-monitor-future", "203.0.113.97")
	target, _ := database.CreateMonitoringTarget("未来时间探针", "probe.example.test:443")
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
