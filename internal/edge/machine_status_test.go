package edge

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"simple_cdn/internal/domain"
)

type machineStatusReporterFunc func() (*domain.MachineStatus, error)

func (function machineStatusReporterFunc) Collect() (*domain.MachineStatus, error) {
	return function()
}

func TestMachineStatusCollectorReportsLinuxHostAndIntervalRates(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	files := map[string]string{
		"/etc/os-release":     "ID=debian\nNAME=\"Debian GNU/Linux\"\nVERSION_ID=\"13\"\n",
		"/etc/debian_version": "13.5\n",
		"/proc/uptime":        "90061.50 123.0\n",
		"/proc/loadavg":       "0.50 0.75 1.25 1/100 42\n",
		"/proc/stat":          "cpu 100 0 50 850 0 0 0 0 0 0\n",
		"/proc/meminfo":       "MemTotal: 8388608 kB\nMemAvailable: 3145728 kB\n",
		"/proc/net/route":     "Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT\neth0 00000000 0100000A 0003 0 0 100 00000000 0 0 0\n",
		"/proc/net/dev":       "Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\neth0: 1000 1 0 0 0 0 0 0 2000 1 0 0 0 0 0 0\nlo: 10 1 0 0 0 0 0 0 10 1 0 0 0 0 0 0\n",
	}
	collector := &machineStatusCollector{
		readFile: func(path string) ([]byte, error) {
			value, found := files[path]
			if !found {
				return nil, errors.New("missing fixture " + path)
			}
			return []byte(value), nil
		},
		statFilesystem: func(string) (int64, int64, error) { return 40 << 30, 100 << 30, nil },
		now:            func() time.Time { return now },
		logicalCPUs:    func() int { return 8 },
	}

	first, err := collector.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if first.Distribution != "Debian GNU/Linux" || first.Version != "13.5" || first.UptimeSeconds != 90061 || first.CPULogicalCores != 8 {
		t.Fatalf("unexpected first machine report: %#v", first)
	}
	if first.MemoryUsedBytes != 5<<30 || first.MemoryTotalBytes != 8<<30 || first.DiskUsedBytes != 40<<30 || first.DiskTotalBytes != 100<<30 {
		t.Fatalf("unexpected capacity report: %#v", first)
	}
	if first.SampleSeconds != 0 || first.CPUUsagePercent != 0 || first.NetworkRXBytesPerSec != 0 || first.NetworkTXBytesPerSec != 0 {
		t.Fatalf("first sample unexpectedly included interval rates: %#v", first)
	}

	now = now.Add(30 * time.Second)
	files["/proc/uptime"] = "90091.50 123.0\n"
	files["/proc/stat"] = "cpu 130 0 70 900 0 0 0 0 0 0\n"
	files["/proc/net/dev"] = "eth0: 4000 1 0 0 0 0 0 0 8000 1 0 0 0 0 0 0\nlo: 20 1 0 0 0 0 0 0 20 1 0 0 0 0 0 0\n"
	second, err := collector.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if second.SampleSeconds != 30 || second.CPUUsagePercent != 50 || second.NetworkInterface != "eth0" || second.NetworkRXBytesPerSec != 100 || second.NetworkTXBytesPerSec != 200 {
		t.Fatalf("unexpected interval report: %#v", second)
	}
	if !domain.ValidMachineStatus(*second) {
		t.Fatalf("collector produced invalid report: %#v", second)
	}
}

func TestHeartbeatIncludesMachineStatusAndCapability(t *testing.T) {
	report := &domain.MachineStatus{
		Distribution: "Debian GNU/Linux", Version: "13.5", UptimeSeconds: 60,
		Load1: 0.1, Load5: 0.2, Load15: 0.3, CPUUsagePercent: 25, CPULogicalCores: 4,
		MemoryUsedBytes: 2 << 30, MemoryTotalBytes: 4 << 30,
		DiskUsedBytes: 10 << 30, DiskTotalBytes: 50 << 30,
		NetworkInterface: "eth0", NetworkRXBytesPerSec: 1000, NetworkTXBytesPerSec: 500,
		SampleSeconds: 30, CollectedAt: time.Now().UTC(),
	}
	var heartbeat struct {
		Capabilities []string              `json:"capabilities"`
		Machine      *domain.MachineStatus `json:"machine_status"`
	}
	client := &http.Client{Transport: upgradeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&heartbeat); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	})}
	agent, err := New(Config{
		ControlURL: "https://control.example.test", StateDir: t.TempDir(), CertificateDir: t.TempDir(),
		AgentSHA256: strings.Repeat("a", 64), HTTPClient: client, Runner: &fakeRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.machineStatus = machineStatusReporterFunc(func() (*domain.MachineStatus, error) {
		copy := *report
		return &copy, nil
	})
	if err := agent.Heartbeat(t.Context(), 1, "", nil); err != nil {
		t.Fatal(err)
	}
	if heartbeat.Machine == nil || heartbeat.Machine.Version != "13.5" || heartbeat.Machine.NetworkRXBytesPerSec != 1000 {
		t.Fatalf("heartbeat machine status = %#v", heartbeat.Machine)
	}
	found := false
	for _, capability := range heartbeat.Capabilities {
		found = found || capability == domain.EdgeCapabilityMachineStatus
	}
	if !found {
		t.Fatalf("heartbeat capabilities = %#v", heartbeat.Capabilities)
	}
	heartbeat.Machine = report
	agent.machineStatus = machineStatusReporterFunc(func() (*domain.MachineStatus, error) {
		return nil, errors.New("procfs unavailable")
	})
	if err := agent.Heartbeat(t.Context(), 1, "", nil); err != nil {
		t.Fatalf("optional machine collection blocked heartbeat: %v", err)
	}
	if heartbeat.Machine != nil {
		t.Fatalf("failed collection sent stale machine status: %#v", heartbeat.Machine)
	}
}
