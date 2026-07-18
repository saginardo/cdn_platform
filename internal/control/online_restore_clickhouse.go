package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type ClickHouseRestoreAdmin interface {
	DatabaseExists(context.Context, string) (bool, error)
	RestoreDatabase(context.Context, string, string, string) error
	ValidateDatabase(context.Context, string) error
	RenameDatabase(context.Context, string, string) error
	DropDatabase(context.Context, string) error
}

type HTTPClickHouseRestoreAdmin struct {
	Endpoint string
	Username string
	Password string
	Client   *http.Client
}

func (a HTTPClickHouseRestoreAdmin) DatabaseExists(ctx context.Context, database string) (bool, error) {
	if !validRestoreIdentifier(database) {
		return false, errors.New("invalid ClickHouse database identifier")
	}
	result, err := a.query(ctx, "SELECT count() FROM system.databases WHERE name = '"+database+"' FORMAT TSVRaw")
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(result) {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("unexpected ClickHouse database count %q", strings.TrimSpace(result))
	}
}

func (a HTTPClickHouseRestoreAdmin) RestoreDatabase(ctx context.Context, sourceDatabase, targetDatabase, diskPath string) error {
	if !validRestoreIdentifier(sourceDatabase) || !validRestoreIdentifier(targetDatabase) || !validRestoreDiskPath(diskPath) {
		return errors.New("invalid ClickHouse restore target")
	}
	_, err := a.query(ctx, "RESTORE DATABASE "+sourceDatabase+" AS "+targetDatabase+" FROM Disk('backups', '"+diskPath+"')")
	return err
}

func (a HTTPClickHouseRestoreAdmin) ValidateDatabase(ctx context.Context, database string) error {
	if !validRestoreIdentifier(database) {
		return errors.New("invalid ClickHouse database identifier")
	}
	engine, err := a.query(ctx, "SELECT engine FROM system.databases WHERE name = '"+database+"' FORMAT TSVRaw")
	if err != nil {
		return err
	}
	if strings.TrimSpace(engine) != "Atomic" {
		return fmt.Errorf("ClickHouse database %s uses %s engine; Atomic is required", database, strings.TrimSpace(engine))
	}
	for _, table := range []string{"cdn_access_logs", "cdn_site_minute", "cdn_access_to_minute"} {
		result, err := a.query(ctx, "SELECT count() FROM system.tables WHERE database = '"+database+"' AND name = '"+table+"' FORMAT TSVRaw")
		if err != nil {
			return err
		}
		if strings.TrimSpace(result) != "1" {
			return fmt.Errorf("ClickHouse database %s is missing table %s", database, table)
		}
	}
	for _, table := range []string{"cdn_access_logs", "cdn_site_minute"} {
		result, err := a.query(ctx, "CHECK TABLE "+database+"."+table+" FORMAT TSVRaw")
		if err != nil {
			return err
		}
		result = strings.TrimSpace(result)
		if result != "1" && !strings.HasPrefix(result, "1\t") {
			return fmt.Errorf("ClickHouse CHECK TABLE failed for %s.%s: %s", database, table, result)
		}
	}
	return nil
}

func (a HTTPClickHouseRestoreAdmin) RenameDatabase(ctx context.Context, source, target string) error {
	if !validRestoreIdentifier(source) || !validRestoreIdentifier(target) {
		return errors.New("invalid ClickHouse database identifier")
	}
	_, err := a.query(ctx, "RENAME DATABASE "+source+" TO "+target)
	return err
}

func (a HTTPClickHouseRestoreAdmin) DropDatabase(ctx context.Context, database string) error {
	if !validRestoreIdentifier(database) {
		return errors.New("invalid ClickHouse database identifier")
	}
	_, err := a.query(ctx, "DROP DATABASE IF EXISTS "+database+" SYNC")
	return err
}

func (a HTTPClickHouseRestoreAdmin) query(ctx context.Context, query string) (string, error) {
	endpoint := strings.TrimSpace(a.Endpoint)
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8123"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/", strings.NewReader(query))
	if err != nil {
		return "", err
	}
	if a.Username != "" {
		request.SetBasicAuth(a.Username, a.Password)
	}
	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("query ClickHouse: %w", err)
	}
	defer response.Body.Close()
	contents, readErr := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if readErr != nil {
		return "", fmt.Errorf("read ClickHouse response: %w", readErr)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := strings.Join(strings.Fields(string(contents)), " ")
		if len(detail) > 1000 {
			detail = detail[:1000]
		}
		return "", fmt.Errorf("ClickHouse returned %s: %s", response.Status, detail)
	}
	return string(contents), nil
}

func validRestoreIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || character == '_' || (index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

func validRestoreDiskPath(value string) bool {
	if value == "" || len(value) > 1000 || strings.HasPrefix(value, "/") || strings.Contains(value, "..") || strings.ContainsAny(value, "'\\\x00") {
		return false
	}
	return true
}
