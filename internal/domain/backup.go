package domain

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultBackupTime               = "03:25"
	DefaultBackupRandomDelaySeconds = 1200
	DefaultBackupRegion             = "us-east-1"
	MaxBackupRandomDelaySeconds     = 86400
)

var backupRegionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}[A-Za-z0-9]$|^[A-Za-z0-9]$`)

type BackupSettings struct {
	Repository         string `json:"repository"`
	AccessKeyID        string `json:"access_key_id"`
	Region             string `json:"region"`
	BackupTime         string `json:"backup_time"`
	RandomDelaySeconds int    `json:"random_delay_seconds"`
}

func NormalizeBackupSettings(settings BackupSettings) BackupSettings {
	settings.Repository = strings.TrimSpace(settings.Repository)
	settings.AccessKeyID = strings.TrimSpace(settings.AccessKeyID)
	settings.Region = strings.TrimSpace(settings.Region)
	settings.BackupTime = strings.TrimSpace(settings.BackupTime)
	if settings.Region == "" {
		settings.Region = DefaultBackupRegion
	}
	if settings.BackupTime == "" {
		settings.BackupTime = DefaultBackupTime
	}
	return settings
}

func ValidateBackupSettings(settings BackupSettings, secretAccessKey, resticPassword string) error {
	settings = NormalizeBackupSettings(settings)
	if len(settings.Repository) > 2048 || !strings.HasPrefix(settings.Repository, "s3:https://") {
		return errors.New("backup repository must use s3:https:// and include a bucket name")
	}
	repositoryURL, err := url.Parse(strings.TrimPrefix(settings.Repository, "s3:"))
	if err != nil || repositoryURL.Scheme != "https" || repositoryURL.Host == "" || strings.Trim(repositoryURL.Path, "/") == "" || repositoryURL.User != nil || repositoryURL.RawQuery != "" || repositoryURL.Fragment != "" {
		return errors.New("backup repository must use s3:https:// and include a bucket name")
	}
	if settings.AccessKeyID == "" || len(settings.AccessKeyID) > 512 || strings.ContainsAny(settings.AccessKeyID, "\r\n\x00") {
		return errors.New("S3 access key ID is invalid")
	}
	if secretAccessKey == "" || len(secretAccessKey) > 4096 || strings.ContainsAny(secretAccessKey, "\r\n\x00") {
		return errors.New("S3 secret access key is invalid")
	}
	if !backupRegionPattern.MatchString(settings.Region) {
		return errors.New("S3 region is invalid")
	}
	if _, err := time.Parse("15:04", settings.BackupTime); err != nil || len(settings.BackupTime) != 5 {
		return errors.New("backup time must use HH:MM")
	}
	if settings.RandomDelaySeconds < 0 || settings.RandomDelaySeconds > MaxBackupRandomDelaySeconds {
		return errors.New("backup random delay must be between 0 and 86400 seconds")
	}
	if resticPassword == "" || len(resticPassword) > 4096 || strings.ContainsAny(resticPassword, "\r\n\x00") {
		return errors.New("Restic repository password is invalid")
	}
	return nil
}
