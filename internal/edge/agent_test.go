package edge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestApplyDoesNotAdvanceVersionWhenReloadIsNotAdopted(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "nginx.conf")
	if err := os.WriteFile(configPath, []byte("old-config"), 0o640); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{applyErr: errors.New("Nginx reload was not adopted"), listeners: [][]domain.PortConflict{nil, nil}}
	agent, err := New(Config{ControlURL: "https://control.example.test", StateDir: directory, NginxConfigPath: configPath, CertificateDir: filepath.Join(directory, "certs"), Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	err = agent.apply(domain.DesiredState{Version: 14, NginxConfig: "new-config"})
	if err == nil || agent.lastApplyReport == nil || agent.lastApplyReport.Code != "nginx_apply_failed" {
		t.Fatalf("apply result = %v, report = %#v", err, agent.lastApplyReport)
	}
	if _, statErr := os.Stat(filepath.Join(directory, "applied-version")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed reload advanced applied version: %v", statErr)
	}
	if contents, readErr := os.ReadFile(configPath); readErr != nil || string(contents) != "old-config" {
		t.Fatalf("old configuration was not restored: %q, %v", contents, readErr)
	}
}

func TestApplyRemovesStaleManagedCertificates(t *testing.T) {
	directory := t.TempDir()
	certificateDir := filepath.Join(directory, "certs")
	if err := os.MkdirAll(certificateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	staleID := "11111111-1111-4111-8111-111111111111"
	desiredID := "22222222-2222-4222-8222-222222222222"
	for _, extension := range []string{".crt", ".key"} {
		if err := os.WriteFile(filepath.Join(certificateDir, staleID+extension), []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(certificateDir, "operator-note.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{listeners: [][]domain.PortConflict{nil, {{Port: 80, PID: 11, Process: "nginx"}, {Port: 443, PID: 12, Process: "nginx"}}}}
	agent, err := New(Config{ControlURL: "https://control.example.test", StateDir: directory, NginxConfigPath: filepath.Join(directory, "nginx.conf"), CertificateDir: certificateDir, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	state := domain.DesiredState{Version: 2, NginxConfig: "new-config", PublicPorts: []int{80, 443}, Certificates: map[string]domain.TLSBundle{
		desiredID: {CertificatePEM: "desired-cert", PrivateKeyPEM: "desired-key"},
	}}
	if err := agent.apply(state); err != nil {
		t.Fatal(err)
	}
	for _, extension := range []string{".crt", ".key"} {
		if _, err := os.Stat(filepath.Join(certificateDir, staleID+extension)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale %s was retained: %v", extension, err)
		}
		if _, err := os.Stat(filepath.Join(certificateDir, desiredID+extension)); err != nil {
			t.Fatalf("desired %s is missing: %v", extension, err)
		}
	}
	if contents, err := os.ReadFile(filepath.Join(certificateDir, "operator-note.txt")); err != nil || string(contents) != "keep" {
		t.Fatalf("unmanaged file changed: %q, %v", contents, err)
	}
}

func TestApplyRestoresRemovedCertificatesOnFailedValidation(t *testing.T) {
	directory := t.TempDir()
	certificateDir := filepath.Join(directory, "certs")
	configPath := filepath.Join(directory, "nginx.conf")
	if err := os.MkdirAll(certificateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("old-config"), 0o640); err != nil {
		t.Fatal(err)
	}
	staleID := "33333333-3333-4333-8333-333333333333"
	for _, extension := range []string{".crt", ".key"} {
		if err := os.WriteFile(filepath.Join(certificateDir, staleID+extension), []byte("stale"+extension), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runner := &fakeRunner{testErr: errors.New("invalid config")}
	agent, err := New(Config{ControlURL: "https://control.example.test", StateDir: directory, NginxConfigPath: configPath, CertificateDir: certificateDir, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.apply(domain.DesiredState{Version: 3, NginxConfig: "bad-config", Certificates: map[string]domain.TLSBundle{}}); err == nil {
		t.Fatal("expected validation failure")
	}
	for _, extension := range []string{".crt", ".key"} {
		contents, err := os.ReadFile(filepath.Join(certificateDir, staleID+extension))
		if err != nil || string(contents) != "stale"+extension {
			t.Fatalf("stale %s was not restored: %q, %v", extension, contents, err)
		}
	}
	if contents, err := os.ReadFile(configPath); err != nil || string(contents) != "old-config" {
		t.Fatalf("old configuration was not restored: %q, %v", contents, err)
	}
	if runner.tests != 2 {
		t.Fatalf("old configuration was not revalidated after certificate restore: tests=%d", runner.tests)
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

func TestApplyWaitsForNginxToOpenNewPublicListener(t *testing.T) {
	directory := t.TempDir()
	runner := &fakeRunner{listeners: [][]domain.PortConflict{
		nil,
		{{Port: 80, PID: 11, Process: "nginx"}},
		{{Port: 80, PID: 11, Process: "nginx"}},
		{{Port: 80, PID: 11, Process: "nginx"}, {Port: 443, PID: 12, Process: "nginx"}},
	}}
	agent, err := New(Config{
		ControlURL: "https://control.example.test", StateDir: directory,
		NginxConfigPath: filepath.Join(directory, "nginx.conf"), CertificateDir: filepath.Join(directory, "certs"), Runner: runner,
		ListenerSettleTimeout: 100 * time.Millisecond, ListenerPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.apply(domain.DesiredState{Version: 10, NginxConfig: "new-config", PublicPorts: []int{80, 443}}); err != nil {
		t.Fatal(err)
	}
	if runner.listenerChecks != 4 || agent.lastApplyReport == nil || agent.lastApplyReport.Status != domain.ApplySucceeded {
		t.Fatalf("delayed listeners were not accepted: checks=%d report=%#v", runner.listenerChecks, agent.lastApplyReport)
	}
}

func TestApplyRollsBackWhenNginxListenerSettleTimesOut(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "nginx.conf")
	if err := os.WriteFile(configPath, []byte("old-config"), 0o640); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{listeners: [][]domain.PortConflict{
		nil,
		{{Port: 80, PID: 11, Process: "nginx"}},
	}}
	agent, err := New(Config{
		ControlURL: "https://control.example.test", StateDir: directory,
		NginxConfigPath: configPath, CertificateDir: filepath.Join(directory, "certs"), Runner: runner,
		ListenerSettleTimeout: 5 * time.Millisecond, ListenerPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = agent.apply(domain.DesiredState{Version: 11, NginxConfig: "new-config", PublicPorts: []int{80, 443}})
	if err == nil || agent.lastApplyReport == nil || agent.lastApplyReport.Code != "nginx_not_listening" {
		t.Fatalf("apply result = %v, report = %#v", err, agent.lastApplyReport)
	}
	config, readErr := os.ReadFile(configPath)
	if readErr != nil || string(config) != "old-config" {
		t.Fatalf("configuration was not restored after listener timeout: %q, %v", config, readErr)
	}
	if runner.listenerChecks < 3 {
		t.Fatalf("listener readiness was not retried: checks=%d", runner.listenerChecks)
	}
}

func TestApplyStopsListenerWaitOnPortConflict(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "nginx.conf")
	if err := os.WriteFile(configPath, []byte("old-config"), 0o640); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{listeners: [][]domain.PortConflict{
		nil,
		{{Port: 80, PID: 11, Process: "nginx"}, {Port: 443, PID: 22, Process: "caddy"}},
	}}
	agent, err := New(Config{
		ControlURL: "https://control.example.test", StateDir: directory,
		NginxConfigPath: configPath, CertificateDir: filepath.Join(directory, "certs"), Runner: runner,
		ListenerSettleTimeout: time.Second, ListenerPollInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = agent.apply(domain.DesiredState{Version: 12, NginxConfig: "new-config", PublicPorts: []int{80, 443}})
	if err == nil || agent.lastApplyReport == nil || agent.lastApplyReport.Code != "port_conflict" {
		t.Fatalf("apply result = %v, report = %#v", err, agent.lastApplyReport)
	}
	if runner.listenerChecks != 2 {
		t.Fatalf("post-reload port conflict should stop waiting immediately: checks=%d", runner.listenerChecks)
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
