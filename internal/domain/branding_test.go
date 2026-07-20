package domain

import (
	"strings"
	"testing"
)

func TestBrandingSettingsDefaultsNormalizationAndValidation(t *testing.T) {
	const logo = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	defaults := DefaultBrandingSettings()
	if defaults.Name != DefaultBrandName || defaults.Subtitle != DefaultBrandSubtitle || defaults.LogoDataURL != "" {
		t.Fatalf("unexpected branding defaults: %#v", defaults)
	}
	if err := ValidateBrandingSettings(defaults); err != nil {
		t.Fatalf("default branding is invalid: %v", err)
	}
	normalized := NormalizeBrandingSettings(BrandingSettings{Name: "  DustK CDN  ", Subtitle: "  运营面板  ", LogoDataURL: "  " + logo + "  "})
	if normalized.Name != "DustK CDN" || normalized.Subtitle != "运营面板" || normalized.LogoDataURL != logo {
		t.Fatalf("unexpected normalized branding: %#v", normalized)
	}
	if err := ValidateBrandingSettings(normalized); err != nil {
		t.Fatalf("valid branding logo was rejected: %v", err)
	}
	for name, settings := range map[string]BrandingSettings{
		"missing name":      {Subtitle: "控制面板"},
		"long name":         {Name: strings.Repeat("品", MaxBrandNameLength+1)},
		"long subtitle":     {Name: "CDN", Subtitle: strings.Repeat("副", MaxBrandSubtitleLength+1)},
		"control character": {Name: "CDN\nPlatform"},
		"unsupported logo":  {Name: "CDN", LogoDataURL: "data:image/svg+xml;base64,PHN2Zz48L3N2Zz4="},
		"invalid logo":      {Name: "CDN", LogoDataURL: "data:image/png;base64,not-base64"},
		"mismatched logo":   {Name: "CDN", LogoDataURL: strings.Replace(logo, "image/png", "image/jpeg", 1)},
		"oversized logo":    {Name: "CDN", LogoDataURL: "data:image/png;base64," + strings.Repeat("A", MaxBrandLogoBytes*2)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateBrandingSettings(settings); err == nil {
				t.Fatalf("invalid branding was accepted: %#v", settings)
			}
		})
	}
}
