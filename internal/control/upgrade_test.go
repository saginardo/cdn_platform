package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/store"
)

func TestNodeOnlineUpgradeAPIQueuesInstructionAndAcceptsEdgeResult(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("edge-online", "203.0.113.93")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeCapabilities(node.ID, []string{domain.EdgeCapabilityOnlineUpgrade}); err != nil {
		t.Fatal(err)
	}
	sourceDigest := strings.Repeat("1", 64)
	targetDigest := strings.Repeat("2", 64)
	if err := database.HeartbeatWithAgent(node.ID, 0, "", nil, sourceDigest, ""); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		Store: database, ControlURL: "https://control.example.test", EdgeControlURL: "https://control.example.test:8443",
		EdgeBinaryURL: "https://control.example.test/downloads/edge", EdgeBinarySHA256: targetDigest,
	}
	startRequest := httptest.NewRequest(http.MethodPost, "/api/nodes/"+node.ID+"/upgrade", nil)
	startRequest.SetPathValue("id", node.ID)
	startResponse := httptest.NewRecorder()
	server.startNodeUpgrade(startResponse, startRequest)
	if startResponse.Code != http.StatusCreated {
		t.Fatalf("start upgrade status = %d, body=%s", startResponse.Code, startResponse.Body.String())
	}
	var status nodeUpgradeStatusResponse
	if err := json.Unmarshal(startResponse.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.UpgradeTask == nil || status.UpgradeTask.Status != domain.NodeUpgradeQueued || status.CanUpgrade {
		t.Fatalf("start upgrade response = %#v", status)
	}

	edgeContext := context.WithValue(context.Background(), edgeContextKey{}, node.ID)
	instructionRequest := httptest.NewRequest(http.MethodGet, "/api/edge/v1/upgrade", nil).WithContext(edgeContext)
	instructionResponse := httptest.NewRecorder()
	server.edgeUpgradeInstruction(instructionResponse, instructionRequest)
	if instructionResponse.Code != http.StatusOK {
		t.Fatalf("instruction status = %d, body=%s", instructionResponse.Code, instructionResponse.Body.String())
	}
	var instruction domain.NodeUpgradeInstruction
	if err := json.Unmarshal(instructionResponse.Body.Bytes(), &instruction); err != nil {
		t.Fatal(err)
	}
	if instruction.TaskID != status.UpgradeTask.ID || instruction.Binary.SHA256 != targetDigest || instruction.UpdaterService.SHA256 == "" {
		t.Fatalf("instruction = %#v", instruction)
	}

	reportBody := strings.NewReader(`{"task_id":"` + instruction.TaskID + `","status":"succeeded","detail":"ready","installed_sha256":"` + targetDigest + `"}`)
	reportRequest := httptest.NewRequest(http.MethodPost, "/api/edge/v1/upgrade-report", reportBody).WithContext(edgeContext)
	reportResponse := httptest.NewRecorder()
	server.edgeUpgradeReport(reportResponse, reportRequest)
	if reportResponse.Code != http.StatusOK {
		t.Fatalf("report status = %d, body=%s", reportResponse.Code, reportResponse.Body.String())
	}
	completed, err := database.NodeUpgradeTask(instruction.TaskID)
	if err != nil || completed.Status != domain.NodeUpgradeSucceeded {
		t.Fatalf("completed task = %#v, err=%v", completed, err)
	}
}

func TestNodeOnlineUpgradeStatusRequiresCapabilityAndFreshHeartbeat(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, _ := database.CreateNode("edge-bootstrap", "203.0.113.94")
	if err := database.HeartbeatWithAgent(node.ID, 0, "", nil, strings.Repeat("1", 64), ""); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: database, EdgeControlURL: "https://control.example.test:8443", EdgeBinaryURL: "https://control.example.test/edge", EdgeBinarySHA256: strings.Repeat("2", 64)}
	node, _ = database.GetNode(node.ID)
	status, err := server.buildNodeUpgradeStatus(node)
	if err != nil {
		t.Fatal(err)
	}
	if status.CanUpgrade || status.UpgradeCapable || !strings.Contains(status.UpgradeBlocker, "手动") {
		t.Fatalf("legacy node status = %#v", status)
	}
}
