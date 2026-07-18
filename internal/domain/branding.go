package domain

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultBrandName       = "CDN Platform"
	DefaultBrandSubtitle   = "控制面板"
	MaxBrandNameLength     = 48
	MaxBrandSubtitleLength = 80
)

type BrandingSettings struct {
	Name     string `json:"name"`
	Subtitle string `json:"subtitle"`
}

func DefaultBrandingSettings() BrandingSettings {
	return BrandingSettings{Name: DefaultBrandName, Subtitle: DefaultBrandSubtitle}
}

func NormalizeBrandingSettings(settings BrandingSettings) BrandingSettings {
	settings.Name = strings.TrimSpace(settings.Name)
	settings.Subtitle = strings.TrimSpace(settings.Subtitle)
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
