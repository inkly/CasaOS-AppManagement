package service

import (
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var nonAlphaNumeric = regexp.MustCompile(`[^a-z0-9]+`)

func Standardize(text string) string {
	if text == "" {
		return "unknown"
	}

	result := strings.ToLower(text)

	// Replace any non-alphanumeric characters with a single hyphen
	result = nonAlphaNumeric.ReplaceAllString(result, "-")

	for strings.Contains(result, "--") {
		result = strings.Replace(result, "--", "-", -1)
	}

	// Remove any leading or trailing hyphens
	result = strings.Trim(result, "-")

	return result
}

// ComposeAppFromSettingsYAML parses the compose YAML the settings page (or any YAML-speaking
// editor) sends back to ApplyComposeAppSettings. Interpolation must stay ON: that YAML came from
// GenerateYAMLFromComposeApp and already has `$` escaped as `$$`; substituteEscaping collapses it
// and escapes the result exactly once (#1988). keep is ComposeApp.EnvKeys(): references to the
// app's `.env` are copied verbatim instead of being resolved.
func ComposeAppFromSettingsYAML(buf []byte, keep map[string]struct{}) (*ComposeApp, error) {
	if keep == nil {
		keep = map[string]struct{}{}
	}
	return newComposeAppFromYAML(buf, false, true, keep)
}

// GenerateYAMLFromComposeApp writes an app loaded by the settings pipeline (ComposeAppFromSettingsYAML,
// LoadComposeAppForEditing) back to compose text; its strings are already escaped for compose.
func GenerateYAMLFromComposeApp(compose ComposeApp) ([]byte, error) {
	return yaml.Marshal(compose)
}
