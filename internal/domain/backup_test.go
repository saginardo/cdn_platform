package domain

import "testing"

func TestValidateBackupSettings(t *testing.T) {
	valid := BackupSettings{
		Repository:         "s3:https://account.r2.cloudflarestorage.com/simple-cdn-backup",
		AccessKeyID:        "access-key",
		Region:             "auto",
		BackupTime:         "03:25",
		RandomDelaySeconds: 1200,
	}
	if err := ValidateBackupSettings(valid, "secret-key", "repository-password"); err != nil {
		t.Fatalf("valid backup settings: %v", err)
	}
	tests := []struct {
		name     string
		settings BackupSettings
		secret   string
		password string
	}{
		{name: "missing bucket", settings: BackupSettings{Repository: "s3:https://account.r2.cloudflarestorage.com", AccessKeyID: "key", Region: "auto", BackupTime: "03:25"}, secret: "secret", password: "password"},
		{name: "plaintext endpoint", settings: BackupSettings{Repository: "s3:http://minio.example.test/bucket", AccessKeyID: "key", Region: "us-east-1", BackupTime: "03:25"}, secret: "secret", password: "password"},
		{name: "invalid region", settings: BackupSettings{Repository: valid.Repository, AccessKeyID: "key", Region: "bad region", BackupTime: "03:25"}, secret: "secret", password: "password"},
		{name: "invalid time", settings: BackupSettings{Repository: valid.Repository, AccessKeyID: "key", Region: "auto", BackupTime: "3:25"}, secret: "secret", password: "password"},
		{name: "excessive delay", settings: BackupSettings{Repository: valid.Repository, AccessKeyID: "key", Region: "auto", BackupTime: "03:25", RandomDelaySeconds: MaxBackupRandomDelaySeconds + 1}, secret: "secret", password: "password"},
		{name: "missing secret", settings: valid, password: "password"},
		{name: "missing password", settings: valid, secret: "secret"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateBackupSettings(test.settings, test.secret, test.password); err == nil {
				t.Fatal("invalid backup settings were accepted")
			}
		})
	}
}
