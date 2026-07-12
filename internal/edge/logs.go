package edge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cdn-platform/internal/domain"
)

const maxLogQueueBytes int64 = 256 << 20

type LogForwarder struct {
	stateDir   string
	logPath    string
	queuePath  string
	offsetPath string
}

func NewLogForwarder(stateDir, logPath string) *LogForwarder {
	return &LogForwarder{stateDir: stateDir, logPath: logPath, queuePath: filepath.Join(stateDir, "access-log-queue.ndjson"), offsetPath: filepath.Join(stateDir, "access-log-offset")}
}

func (f *LogForwarder) Collect() (int, error) {
	file, err := os.Open(f.logPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	offset := f.offset()
	if info.Size() < offset {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(f.stateDir, 0o750); err != nil {
		return 0, err
	}
	queue, err := os.OpenFile(f.queuePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	defer queue.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	count := 0
	position := offset
	for scanner.Scan() {
		line := scanner.Bytes()
		position += int64(len(line) + 1)
		event, err := decodeNginxLog(line)
		if err != nil {
			continue
		}
		serialized, err := json.Marshal(event)
		if err != nil {
			return count, err
		}
		if _, err := queue.Write(append(serialized, '\n')); err != nil {
			return count, err
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return count, err
	}
	if err := atomicWriteFile(f.offsetPath, []byte(strconv.FormatInt(position, 10)), 0o640); err != nil {
		return count, err
	}
	if queueInfo, err := queue.Stat(); err == nil && queueInfo.Size() > maxLogQueueBytes {
		return count, fmt.Errorf("access-log queue exceeds %d bytes; resolve control-plane log delivery", maxLogQueueBytes)
	}
	return count, nil
}

func (f *LogForwarder) Flush(ctx context.Context, controlURL string, client *http.Client) error {
	contents, err := os.ReadFile(f.queuePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	lines := bytes.Split(contents, []byte{'\n'})
	var events []domain.AccessLogEvent
	consumedLines := 0
	for _, line := range lines {
		if len(line) == 0 {
			consumedLines++
			continue
		}
		if len(events) >= 500 {
			break
		}
		var event domain.AccessLogEvent
		consumedLines++
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	if consumedLines == 0 {
		return nil
	}
	if len(events) == 0 {
		return atomicWriteFile(f.queuePath, joinLines(lines[consumedLines:]), 0o640)
	}
	payload, err := json.Marshal(events)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(controlURL, "/")+"/api/edge/v1/logs", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("log upload: %s", response.Status)
	}
	return atomicWriteFile(f.queuePath, joinLines(lines[consumedLines:]), 0o640)
}

func (f *LogForwarder) offset() int64 {
	contents, err := os.ReadFile(f.offsetPath)
	if err != nil {
		return 0
	}
	value, _ := strconv.ParseInt(strings.TrimSpace(string(contents)), 10, 64)
	return value
}

type nginxLog struct {
	Timestamp       string      `json:"timestamp"`
	SiteID          string      `json:"site_id"`
	ClientIP        string      `json:"client_ip"`
	Method          string      `json:"method"`
	Path            string      `json:"path"`
	Status          int         `json:"status"`
	Bytes           int64       `json:"bytes"`
	DurationSeconds json.Number `json:"duration_seconds"`
	Upstream        string      `json:"upstream"`
	CacheStatus     string      `json:"cache_status"`
}

func decodeNginxLog(line []byte) (domain.AccessLogEvent, error) {
	var raw nginxLog
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return domain.AccessLogEvent{}, err
	}
	timestamp, err := time.Parse(time.RFC3339, raw.Timestamp)
	if err != nil {
		return domain.AccessLogEvent{}, err
	}
	duration, _ := raw.DurationSeconds.Float64()
	return domain.AccessLogEvent{Timestamp: timestamp, SiteID: raw.SiteID, ClientIP: raw.ClientIP, Method: raw.Method, Path: strings.SplitN(raw.Path, "?", 2)[0], Status: raw.Status, Bytes: raw.Bytes, DurationMS: int64(duration * 1000), Upstream: raw.Upstream, CacheStatus: raw.CacheStatus}, nil
}

func joinLines(lines [][]byte) []byte {
	if len(lines) == 0 {
		return nil
	}
	return bytes.Join(lines, []byte{'\n'})
}
