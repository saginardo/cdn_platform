package edge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cdn-platform/internal/domain"
)

type fakeRunner struct {
	testErr        error
	applyErr       error
	listeners      [][]domain.PortConflict
	tests          int
	applies        int
	listenerChecks int
}

func (f *fakeRunner) Test() error { f.tests++; return f.testErr }
func (f *fakeRunner) Apply() error {
	f.applies++
	return f.applyErr
}
func (f *fakeRunner) PortListeners(_ []int) ([]domain.PortConflict, error) {
	index := f.listenerChecks
	f.listenerChecks++
	if index >= len(f.listeners) {
		return nil, nil
	}
	return f.listeners[index], nil
}

func TestApplyRollsBackConfigAndCertificatesOnFailedValidation(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "nginx.conf")
	certificateDir := filepath.Join(directory, "certs")
	if err := os.MkdirAll(certificateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("old-config"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certificateDir, "site.crt"), []byte("old-cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{testErr: errors.New("invalid config")}
	agent, err := New(Config{ControlURL: "https://control.example.test", StateDir: directory, NginxConfigPath: configPath, CertificateDir: certificateDir, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	err = agent.apply(domain.DesiredState{Version: 1, NginxConfig: "bad-config", Certificates: map[string]domain.TLSBundle{"site": {CertificatePEM: "new-cert", PrivateKeyPEM: "new-key"}}})
	if err == nil {
		t.Fatal("expected Nginx validation error")
	}
	config, _ := os.ReadFile(configPath)
	certificate, _ := os.ReadFile(filepath.Join(certificateDir, "site.crt"))
	if string(config) != "old-config" || string(certificate) != "old-cert" {
		t.Fatalf("state was not restored: config=%q certificate=%q", config, certificate)
	}
	if runner.applies != 0 {
		t.Fatalf("apply should not run after failed validation")
	}
}

func TestApplyReportsPortConflictWithoutChangingConfiguration(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "nginx.conf")
	if err := os.WriteFile(configPath, []byte("old-config"), 0o640); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{listeners: [][]domain.PortConflict{{{Port: 80, PID: 1234, Process: "caddy"}}}}
	agent, err := New(Config{ControlURL: "https://control.example.test", StateDir: directory, NginxConfigPath: configPath, CertificateDir: filepath.Join(directory, "certs"), Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	err = agent.apply(domain.DesiredState{Version: 7, NginxConfig: "new-config"})
	if err == nil || agent.lastApplyReport == nil || agent.lastApplyReport.Code != "port_conflict" {
		t.Fatalf("apply result = %v, report = %#v", err, agent.lastApplyReport)
	}
	if runner.tests != 0 || runner.applies != 0 {
		t.Fatalf("port conflict should stop before Nginx work: tests=%d applies=%d", runner.tests, runner.applies)
	}
	config, err := os.ReadFile(configPath)
	if err != nil || string(config) != "old-config" {
		t.Fatalf("configuration changed after conflict: %q, %v", config, err)
	}
}

func TestApplyConfirmsNginxOwnsBothPublicPorts(t *testing.T) {
	directory := t.TempDir()
	runner := &fakeRunner{listeners: [][]domain.PortConflict{
		nil,
		{{Port: 80, PID: 11, Process: "nginx"}, {Port: 443, PID: 12, Process: "nginx"}},
	}}
	agent, err := New(Config{ControlURL: "https://control.example.test", StateDir: directory, NginxConfigPath: filepath.Join(directory, "nginx.conf"), CertificateDir: filepath.Join(directory, "certs"), Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.apply(domain.DesiredState{Version: 8, NginxConfig: "new-config", PublicPorts: []int{80, 443}}); err != nil {
		t.Fatal(err)
	}
	if runner.tests != 1 || runner.applies != 1 || agent.lastApplyReport == nil || agent.lastApplyReport.Status != domain.ApplySucceeded {
		t.Fatalf("unexpected apply state: tests=%d applies=%d report=%#v", runner.tests, runner.applies, agent.lastApplyReport)
	}
}

func TestApplyAllowsHealthOnlyNodeToListenOnPort80(t *testing.T) {
	directory := t.TempDir()
	runner := &fakeRunner{listeners: [][]domain.PortConflict{
		nil,
		{{Port: 80, PID: 11, Process: "nginx"}},
	}}
	agent, err := New(Config{ControlURL: "https://control.example.test", StateDir: directory, NginxConfigPath: filepath.Join(directory, "nginx.conf"), CertificateDir: filepath.Join(directory, "certs"), Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.apply(domain.DesiredState{Version: 9, NginxConfig: "health-config", PublicPorts: []int{80}}); err != nil {
		t.Fatal(err)
	}
	if agent.lastApplyReport == nil || agent.lastApplyReport.Status != domain.ApplySucceeded || !strings.Contains(agent.lastApplyReport.Detail, "TCP 80") {
		t.Fatalf("unexpected report: %#v", agent.lastApplyReport)
	}
}

func TestNewRejectsInsecureControlURL(t *testing.T) {
	if _, err := New(Config{ControlURL: "http://control.example.test", StateDir: t.TempDir(), Runner: &fakeRunner{}}); err == nil {
		t.Fatal("expected HTTP control URL to be rejected")
	}
}
