package porter

import (
	"testing"

	"github.com/delivery-station/ds/pkg/types"
)

func TestNormalizeLoggingConfigDefaults(t *testing.T) {
	normalized := NormalizeLoggingConfig(types.LoggingConfig{}, "")

	if normalized.Level != "info" {
		t.Fatalf("expected default level info, got %s", normalized.Level)
	}
	if !normalized.LevelValid {
		t.Fatalf("expected default level to be considered valid")
	}
	if normalized.Format != "text" {
		t.Fatalf("expected default format text, got %s", normalized.Format)
	}
	if !normalized.FormatValid {
		t.Fatalf("expected default format to be considered valid")
	}
	if normalized.Output != "" {
		t.Fatalf("expected empty output, got %s", normalized.Output)
	}
}

func TestNormalizeLoggingConfigInvalidValues(t *testing.T) {
	cfg := types.LoggingConfig{Level: "LOUD", Format: "xml", Output: " /tmp/logs/ds-porter.log "}
	normalized := NormalizeLoggingConfig(cfg, "")

	if normalized.Level != "info" {
		t.Fatalf("expected fallback level info, got %s", normalized.Level)
	}
	if normalized.LevelValid {
		t.Fatalf("expected LevelValid to be false for invalid input")
	}
	if normalized.Format != "text" {
		t.Fatalf("expected fallback format text, got %s", normalized.Format)
	}
	if normalized.FormatValid {
		t.Fatalf("expected FormatValid to be false for invalid input")
	}
	if normalized.Output != "/tmp/logs/ds-porter.log" {
		t.Fatalf("expected trimmed output path, got %s", normalized.Output)
	}
}

func TestNormalizeLoggingConfigFallbackLevel(t *testing.T) {
	cfg := types.LoggingConfig{Level: "", Format: "json"}
	normalized := NormalizeLoggingConfig(cfg, "debug")

	if normalized.Level != "debug" {
		t.Fatalf("expected level to use fallback value, got %s", normalized.Level)
	}
	if !normalized.LevelValid {
		t.Fatalf("expected fallback level to be considered valid")
	}
	if !normalized.IsJSON() {
		t.Fatalf("expected json format to be detected")
	}
}
