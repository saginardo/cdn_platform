package domain

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultBrandName       = "simple_cdn"
	DefaultBrandSubtitle   = "控制面板"
	MaxBrandNameLength     = 48
	MaxBrandSubtitleLength = 80
	MaxBrandLogoBytes      = 128 << 10
	MaxBrandLogoDimension  = 1024
)

type BrandingSettings struct {
	Name        string `json:"name"`
	Subtitle    string `json:"subtitle"`
	LogoDataURL string `json:"logo_data_url"`
}

func DefaultBrandingSettings() BrandingSettings {
	return BrandingSettings{Name: DefaultBrandName, Subtitle: DefaultBrandSubtitle}
}

func NormalizeBrandingSettings(settings BrandingSettings) BrandingSettings {
	settings.Name = strings.TrimSpace(settings.Name)
	settings.Subtitle = strings.TrimSpace(settings.Subtitle)
	settings.LogoDataURL = strings.TrimSpace(settings.LogoDataURL)
	return settings
}

func ValidateBrandingSettings(settings BrandingSettings) error {
	settings = NormalizeBrandingSettings(settings)
	if settings.Name == "" {
		return errors.New("brand name is required")
	}
	if utf8.RuneCountInString(settings.Name) > MaxBrandNameLength {
		return errors.New("brand name is too long")
	}
	if utf8.RuneCountInString(settings.Subtitle) > MaxBrandSubtitleLength {
		return errors.New("brand subtitle is too long")
	}
	if containsControlCharacter(settings.Name) || containsControlCharacter(settings.Subtitle) {
		return errors.New("brand settings contain invalid control characters")
	}
	if err := validateBrandLogoDataURL(settings.LogoDataURL); err != nil {
		return err
	}
	return nil
}

func validateBrandLogoDataURL(value string) error {
	if value == "" {
		return nil
	}
	metadata, encoded, ok := strings.Cut(value, ";base64,")
	if !ok {
		return errors.New("brand logo must be a base64-encoded PNG or JPEG image")
	}
	mediaType := strings.TrimPrefix(metadata, "data:")
	expectedFormat := ""
	switch mediaType {
	case "image/png":
		expectedFormat = "png"
	case "image/jpeg":
		expectedFormat = "jpeg"
	default:
		return errors.New("brand logo must be a PNG or JPEG image")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(MaxBrandLogoBytes) {
		return errors.New("brand logo is too large")
	}
	contents, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(contents) == 0 {
		return errors.New("brand logo contains invalid base64 image data")
	}
	if len(contents) > MaxBrandLogoBytes {
		return errors.New("brand logo is too large")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(contents))
	if err != nil || format != expectedFormat {
		return errors.New("brand logo image data does not match its media type")
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > MaxBrandLogoDimension || config.Height > MaxBrandLogoDimension {
		return errors.New("brand logo dimensions must not exceed 1024 by 1024 pixels")
	}
	return nil
}

func containsControlCharacter(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
