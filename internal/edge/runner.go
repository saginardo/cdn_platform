package edge

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"cdn-platform/internal/domain"
)

type Runner interface {
	Test() error
	Apply() error
	PortListeners(ports []int) ([]domain.PortConflict, error)
}

type NginxRunner struct {
	Binary string
}

func (r NginxRunner) Test() error {
	binary := r.Binary
	if binary == "" {
		binary = "nginx"
	}
	output, err := exec.Command(binary, "-t").CombinedOutput()
	if err != nil {
		return fmt.Errorf("nginx -t: %w: %s", err, output)
	}
	return nil
}

func (r NginxRunner) Apply() error {
	active := exec.Command("systemctl", "is-active", "--quiet", "nginx").Run() == nil
	if active {
		return r.reload()
	}
	// A failed unit retains its failed state after a previous port conflict.
	// Clearing it here makes a later Publish a real recovery action once the
	// conflicting listener has been removed.
	_ = exec.Command("systemctl", "reset-failed", "nginx").Run()
	output, err := exec.Command("systemctl", "start", "nginx").CombinedOutput()
	if err != nil {
		return fmt.Errorf("start nginx: %w: %s", err, output)
	}
	return nil
}

func (r NginxRunner) reload() error {
	binary := r.Binary
	if binary == "" {
		binary = "nginx"
	}
	output, err := exec.Command(binary, "-s", "reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("nginx reload: %w: %s", err, output)
	}
	return nil
}

var listenerProcess = regexp.MustCompile(`\("([^"]+)",pid=([0-9]+)`) // ss users:(()) output

func (r NginxRunner) PortListeners(ports []int) ([]domain.PortConflict, error) {
	seen := make(map[string]domain.PortConflict)
	for _, port := range ports {
		output, err := exec.Command("ss", "-H", "-ltnp", fmt.Sprintf("( sport = :%d )", port)).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("inspect TCP port %d: %w: %s", port, err, output)
		}
		matches := listenerProcess.FindAllStringSubmatch(string(output), -1)
		if len(matches) == 0 && strings.TrimSpace(string(output)) != "" {
			key := fmt.Sprintf("%d:unknown:0", port)
			seen[key] = domain.PortConflict{Port: port, Process: "unknown"}
		}
		for _, match := range matches {
			pid, _ := strconv.Atoi(match[2])
			listener := domain.PortConflict{Port: port, PID: pid, Process: match[1]}
			key := fmt.Sprintf("%d:%s:%d", listener.Port, listener.Process, listener.PID)
			seen[key] = listener
		}
	}
	listeners := make([]domain.PortConflict, 0, len(seen))
	for _, listener := range seen {
		listeners = append(listeners, listener)
	}
	sort.Slice(listeners, func(i, j int) bool {
		if listeners[i].Port != listeners[j].Port {
			return listeners[i].Port < listeners[j].Port
		}
		if listeners[i].Process != listeners[j].Process {
			return listeners[i].Process < listeners[j].Process
		}
		return listeners[i].PID < listeners[j].PID
	})
	return listeners, nil
}
