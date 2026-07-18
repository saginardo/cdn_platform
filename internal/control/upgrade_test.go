package control

import (
	"context"
	"encoding/json"
	"errors"
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

func TestStartAllNodeUpgradesQueuesOnlyEligibleOutdatedNodes(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	sourceDigest := strings.Repeat("1", 64)
	targetDigest := strings.Repeat("2", 64)
	eligible, _ := database.CreateNode("eligible", "203.0.113.101")
	current, _ := database.CreateNode("current", "203.0.113.102")
	blocked, _ := database.CreateNode("blocked", "203.0.113.103")
	for _, node := range []domain.Node{eligible, current} {
		if err := database.SetNodeCapabilities(node.ID, []string{domain.EdgeCapabilityOnlineUpgrade}); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.HeartbeatWithAgent(eligible.ID, 0, "", nil, sourceDigest, ""); err != nil {
		t.Fatal(err)
	}
	if err := database.HeartbeatWithAgent(current.ID, 0, "", nil, targetDigest, ""); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		Store: database, EdgeControlURL: "https://control.example.test:8443",
		EdgeBinaryURL: "https://control.example.test/edge", EdgeBinarySHA256: targetDigest,
	}
	request := httptest.NewRequest(http.MethodPost, "/api/nodes/upgrade-all", nil)
	response := httptest.NewRecorder()
	server.startAllNodeUpgrades(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("bulk upgrade status = %d, body=%s", response.Code, response.Body.String())
	}
	var result nodeUpgradeAllResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.UpToDate != 1 || result.Blocked != 1 || len(result.Results) != 3 {
		t.Fatalf("bulk upgrade result = %#v", result)
	}
	if _, err := database.LatestNodeUpgrade(eligible.ID); err != nil {
		t.Fatalf("eligible node was not queued: %v", err)
	}
	if _, err := database.LatestNodeUpgrade(current.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("current node received an upgrade: %v", err)
	}
	if _, err := database.LatestNodeUpgrade(blocked.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("blocked node received an upgrade: %v", err)
	}
}
