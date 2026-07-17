package store

import (
	"path/filepath"
	"testing"
	"time"

	"cdn-platform/internal/domain"
)

func TestNodeMachineStatusKeepsNewestCollectedReport(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("machine-edge", "203.0.113.80")
	if err != nil {
		t.Fatal(err)
	}
	newest := testMachineStatus(time.Now().UTC().Truncate(time.Second))
	newest.CPUUsagePercent = 70
	if err := database.RecordNodeMachineStatus(node.ID, newest); err != nil {
		t.Fatal(err)
	}
	older := testMachineStatus(newest.CollectedAt.Add(-time.Minute))
	older.CPUUsagePercent = 10
	if err := database.RecordNodeMachineStatus(node.ID, older); err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetNodeMachineStatus(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CPUUsagePercent != 70 || !stored.CollectedAt.Equal(newest.CollectedAt) || stored.Version != "13.5" {
		t.Fatalf("stored machine status = %#v", stored)
	}
	invalid := newest
	invalid.MemoryUsedBytes = invalid.MemoryTotalBytes + 1
	if err := database.RecordNodeMachineStatus(node.ID, invalid); err == nil {
		t.Fatal("invalid machine status was stored")
	}
}

func testMachineStatus(collectedAt time.Time) domain.MachineStatus {
	return domain.MachineStatus{
		Distribution: "Debian GNU/Linux", Version: "13.5", UptimeSeconds: 86400,
		Load1: 0.1, Load5: 0.2, Load15: 0.3, CPUUsagePercent: 25, CPULogicalCores: 4,
		MemoryUsedBytes: 2 << 30, MemoryTotalBytes: 4 << 30,
		DiskUsedBytes: 20 << 30, DiskTotalBytes: 100 << 30,
		NetworkInterface: "eth0", NetworkRXBytesPerSec: 2000, NetworkTXBytesPerSec: 1000,
		SampleSeconds: 30, CollectedAt: collectedAt,
	}
}
