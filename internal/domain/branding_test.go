package domain

import (
	"strings"
	"testing"
)

func TestBrandingSettingsDefaultsNormalizationAndValidation(t *testing.T) {
	defaults := DefaultBrandingSettings()
	if defaults.Name != DefaultBrandName || defaults.Subtitle != DefaultBrandSubtitle {
		t.Fatalf("unexpected branding defaults: %#v", defaults)
	}
	if err := ValidateBrandingSettings(defaults); err != nil {
		t.Fatalf("default branding is invalid: %v", err)
	}
	normalized := NormalizeBrandingSettings(BrandingSettings{Name: "  DustK CDN  ", Subtitle: "  运营面板  "})
	if normalized.Name != "DustK CDN" || normalized.Subtitle != "运营面板" {
		t.Fatalf("unexpected normalized branding: %#v", normalized)
	}
	for name, settings := range map[string]BrandingSettings{
		"missing name":      {Subtitle: "控制面板"},
		"long name":         {Name: strings.Repeat("品", MaxBrandNameLength+1)},
		"long subtitle":     {Name: "CDN", Subtitle: strings.Repeat("副", MaxBrandSubtitleLength+1)},
		"control character": {Name: "CDN\nPlatform"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateBrandingSettings(settings); err == nil {
				t.Fatalf("invalid branding was accepted: %#v", settings)
			}
		})
	}
}
