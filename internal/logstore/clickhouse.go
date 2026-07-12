package logstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cdn-platform/internal/domain"
)

type Store interface {
	Append(ctx context.Context, events []domain.AccessLogEvent) error
	Recent(ctx context.Context, siteID string, limit int) ([]domain.AccessLogEvent, error)
	Metrics(ctx context.Context, siteID string, since time.Time) ([]MinuteMetric, error)
}

type MinuteMetric struct {
	Minute    time.Time `json:"minute"`
	Requests  uint64    `json:"requests"`
	Bytes     int64     `json:"bytes"`
	Errors    uint64    `json:"errors"`
	CacheHits uint64    `json:"cache_hits"`
}

type ClickHouse struct {
	Endpoint string
	Database string
	Username string
	Password string
	Client   *http.Client
}

func (c ClickHouse) EnsureSchema(ctx context.Context) error {
	if err := c.queryInDatabase(ctx, "default", `CREATE DATABASE IF NOT EXISTS `+identifier(c.database()), nil); err != nil {
		return err
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ` + identifier(c.database()) + `.cdn_access_logs (
 timestamp DateTime64(3, 'UTC'), node_id String, site_id String, client_ip String, method LowCardinality(String), path String, status UInt16, bytes Int64, duration_ms Int64, upstream String, cache_status LowCardinality(String)
) ENGINE = MergeTree PARTITION BY toDate(timestamp) ORDER BY (site_id, timestamp, node_id) TTL timestamp + INTERVAL 7 DAY DELETE`,
		`CREATE TABLE IF NOT EXISTS ` + identifier(c.database()) + `.cdn_site_minute (
 minute DateTime('UTC'), site_id String, node_id String, requests UInt64, bytes Int64, errors UInt64, cache_hits UInt64
) ENGINE = SummingMergeTree PARTITION BY toDate(minute) ORDER BY (site_id, minute, node_id) TTL minute + INTERVAL 30 DAY DELETE`,
		`CREATE MATERIALIZED VIEW IF NOT EXISTS ` + identifier(c.database()) + `.cdn_access_to_minute TO ` + identifier(c.database()) + `.cdn_site_minute AS
 SELECT toStartOfMinute(timestamp) AS minute, site_id, node_id, count() AS requests, sum(bytes) AS bytes, countIf(status >= 500) AS errors, countIf(cache_status = 'HIT') AS cache_hits
 FROM ` + identifier(c.database()) + `.cdn_access_logs GROUP BY minute, site_id, node_id`,
	}
	for _, statement := range statements {
		if err := c.query(ctx, statement, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c ClickHouse) Append(ctx context.Context, events []domain.AccessLogEvent) error {
	if len(events) == 0 {
		return nil
	}
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	for _, event := range events {
		if event.Timestamp.IsZero() {
			event.Timestamp = time.Now().UTC()
		}
		event.Path = stripQuery(event.Path)
		row := accessLogInsert{
			Timestamp:   event.Timestamp.UTC().Format("2006-01-02 15:04:05.000"),
			NodeID:      event.NodeID,
			SiteID:      event.SiteID,
			ClientIP:    event.ClientIP,
			Method:      event.Method,
			Path:        event.Path,
			Status:      event.Status,
			Bytes:       event.Bytes,
			DurationMS:  event.DurationMS,
			Upstream:    event.Upstream,
			CacheStatus: event.CacheStatus,
		}
		if err := encoder.Encode(row); err != nil {
			return err
		}
	}
	return c.query(ctx, "INSERT INTO "+identifier(c.database())+".cdn_access_logs FORMAT JSONEachRow", &body)
}

func (c ClickHouse) Recent(ctx context.Context, siteID string, limit int) ([]domain.AccessLogEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT timestamp, node_id, site_id, client_ip, method, path, status, bytes, duration_ms, upstream, cache_status FROM ` + identifier(c.database()) + `.cdn_access_logs WHERE site_id = {site_id:String} ORDER BY timestamp DESC LIMIT ` + strconv.Itoa(limit) + ` FORMAT JSONEachRow`
	parameters := url.Values{"param_site_id": {siteID}}
	response, err := c.request(ctx, c.database(), query, nil, parameters)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	events := make([]domain.AccessLogEvent, 0)
	decoder := json.NewDecoder(response.Body)
	for {
		var row accessLogRow
		if err := decoder.Decode(&row); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		event, err := row.event()
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (c ClickHouse) Metrics(ctx context.Context, siteID string, since time.Time) ([]MinuteMetric, error) {
	query := `SELECT minute, sum(requests) AS requests, sum(bytes) AS bytes, sum(errors) AS errors, sum(cache_hits) AS cache_hits FROM ` + identifier(c.database()) + `.cdn_site_minute WHERE site_id = {site_id:String} AND minute >= {since:DateTime} GROUP BY minute ORDER BY minute FORMAT JSONEachRow`
	parameters := url.Values{"param_site_id": {siteID}, "param_since": {since.UTC().Format("2006-01-02 15:04:05")}}
	response, err := c.request(ctx, c.database(), query, nil, parameters)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	metrics := make([]MinuteMetric, 0)
	decoder := json.NewDecoder(response.Body)
	for {
		var metric MinuteMetric
		if err := decoder.Decode(&metric); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		metrics = append(metrics, metric)
	}
	return metrics, nil
}

func (c ClickHouse) query(ctx context.Context, query string, body *bytes.Buffer) error {
	return c.queryInDatabase(ctx, c.database(), query, body)
}

func (c ClickHouse) queryInDatabase(ctx context.Context, database, query string, body *bytes.Buffer) error {
	response, err := c.request(ctx, database, query, body, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("ClickHouse %s", response.Status)
	}
	return nil
}

func (c ClickHouse) request(ctx context.Context, database, query string, body *bytes.Buffer, parameters url.Values) (*http.Response, error) {
	values := url.Values{"query": {query}}
	if database != "" {
		values.Set("database", database)
	}
	for key, value := range parameters {
		values[key] = value
	}
	endpoint := strings.TrimRight(c.Endpoint, "/")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8123"
	}
	var reader io.Reader
	if body != nil {
		reader = body
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/?"+values.Encode(), reader)
	if err != nil {
		return nil, err
	}
	if c.Username != "" || c.Password != "" {
		request.SetBasicAuth(c.Username, c.Password)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, fmt.Errorf("ClickHouse returned %s", response.Status)
	}
	return response, nil
}

func (c ClickHouse) database() string {
	if c.Database != "" {
		return c.Database
	}
	return "cdn_platform"
}

func identifier(value string) string {
	if value == "" {
		return "`cdn_platform`"
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_') {
			panic("unsafe ClickHouse identifier")
		}
	}
	return "`" + value + "`"
}

func stripQuery(path string) string {
	if position := strings.IndexByte(path, '?'); position >= 0 {
		return path[:position]
	}
	return path
}

type Noop struct{}

func (Noop) Append(context.Context, []domain.AccessLogEvent) error { return nil }
func (Noop) Recent(context.Context, string, int) ([]domain.AccessLogEvent, error) {
	return []domain.AccessLogEvent{}, nil
}
func (Noop) Metrics(context.Context, string, time.Time) ([]MinuteMetric, error) {
	return []MinuteMetric{}, nil
}

type accessLogRow struct {
	Timestamp   string `json:"timestamp"`
	NodeID      string `json:"node_id"`
	SiteID      string `json:"site_id"`
	ClientIP    string `json:"client_ip"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Status      int    `json:"status"`
	Bytes       int64  `json:"bytes"`
	DurationMS  int64  `json:"duration_ms"`
	Upstream    string `json:"upstream"`
	CacheStatus string `json:"cache_status"`
}

type accessLogInsert struct {
	Timestamp   string `json:"timestamp"`
	NodeID      string `json:"node_id"`
	SiteID      string `json:"site_id"`
	ClientIP    string `json:"client_ip"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Status      int    `json:"status"`
	Bytes       int64  `json:"bytes"`
	DurationMS  int64  `json:"duration_ms"`
	Upstream    string `json:"upstream"`
	CacheStatus string `json:"cache_status"`
}

func (r accessLogRow) event() (domain.AccessLogEvent, error) {
	timestamp, err := parseClickHouseTime(r.Timestamp)
	if err != nil {
		return domain.AccessLogEvent{}, fmt.Errorf("decode access-log timestamp: %w", err)
	}
	return domain.AccessLogEvent{Timestamp: timestamp, NodeID: r.NodeID, SiteID: r.SiteID, ClientIP: r.ClientIP, Method: r.Method, Path: r.Path, Status: r.Status, Bytes: r.Bytes, DurationMS: r.DurationMS, Upstream: r.Upstream, CacheStatus: r.CacheStatus}, nil
}

func (m *MinuteMetric) UnmarshalJSON(contents []byte) error {
	var row struct {
		Minute    string `json:"minute"`
		Requests  uint64 `json:"requests"`
		Bytes     int64  `json:"bytes"`
		Errors    uint64 `json:"errors"`
		CacheHits uint64 `json:"cache_hits"`
	}
	if err := json.Unmarshal(contents, &row); err != nil {
		return err
	}
	minute, err := parseClickHouseTime(row.Minute)
	if err != nil {
		return fmt.Errorf("decode minute metric timestamp: %w", err)
	}
	*m = MinuteMetric{Minute: minute, Requests: row.Requests, Bytes: row.Bytes, Errors: row.Errors, CacheHits: row.CacheHits}
	return nil
}

func parseClickHouseTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC(), nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}
