package porter

import (
	"strings"

	"github.com/delivery-station/ds/pkg/types"
	"github.com/hashicorp/go-hclog"
)

// NormalizedLogging represents a sanitized logging configuration with defaults applied.
type NormalizedLogging struct {
	Level       string
	Format      string
	Output      string
	LevelValid  bool
	FormatValid bool
}

// NormalizeLoggingConfig converts a DS logging configuration into a normalized form with
// defaults applied. It also reports whether the original level and format were valid.
func NormalizeLoggingConfig(logging types.LoggingConfig, fallbackLevel string) NormalizedLogging {
	level := strings.ToLower(strings.TrimSpace(logging.Level))
	fallback := strings.ToLower(strings.TrimSpace(fallbackLevel))
	if level == "" {
		level = fallback
	}

	levelValid := true
	if level == "" {
		level = "info"
	} else if hclog.LevelFromString(level) == hclog.NoLevel {
		levelValid = false
		level = "info"
	}

	format := strings.ToLower(strings.TrimSpace(logging.Format))
	formatValid := true
	if format == "" {
		format = "text"
	} else if format != "json" && format != "text" {
		formatValid = false
		format = "text"
	}

	output := strings.TrimSpace(logging.Output)

	return NormalizedLogging{
		Level:       level,
		Format:      format,
		Output:      output,
		LevelValid:  levelValid,
		FormatValid: formatValid,
	}
}

// ApplyLogLevel ensures the logger uses the normalized log level.
func ApplyLogLevel(logger hclog.Logger, normalized NormalizedLogging) {
	if logger == nil {
		return
	}

	lvl := hclog.LevelFromString(normalized.Level)
	if lvl == hclog.NoLevel {
		return
	}

	logger.SetLevel(lvl)
}

// Equal returns true if both normalized configurations are identical.
func (n NormalizedLogging) Equal(other NormalizedLogging) bool {
	return n.Level == other.Level &&
		n.Format == other.Format &&
		n.Output == other.Output &&
		n.LevelValid == other.LevelValid &&
		n.FormatValid == other.FormatValid
}

// IsJSON reports whether the normalized format is JSON.
func (n NormalizedLogging) IsJSON() bool {
	return n.Format == "json"
}
