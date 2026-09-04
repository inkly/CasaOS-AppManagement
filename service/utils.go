package service

import (
	"regexp"
	"strings"

	"github.com/IceWhaleTech/CasaOS-Common/utils"
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

// ComposeAppFromSettingsYAML parses the compose YAML the settings page (and the raw compose
// editor) sends back to ApplyComposeAppSettings. Interpolation must stay ON: that YAML came from
// GenerateYAMLFromComposeApp and already has `$` escaped as `$$`, so interpolating collapses it
// back to `$` and the generator re-escapes it exactly once. skipInterpolation=true doubled it on
// every save (#1988).
func ComposeAppFromSettingsYAML(buf []byte) (*ComposeApp, error) {
	return NewComposeAppFromYAML(buf, false, true)
}

func GenerateYAMLFromComposeApp(compose ComposeApp) ([]byte, error) {
	// to duplicate Specify Chars
	for _, service := range compose.Services {
		// it should duplicate all values that contains $. But for now, we only duplicate the env values
		for key, value := range service.Environment {
			if strings.ContainsAny(*value, "$") {
				service.Environment[key] = utils.Ptr(strings.Replace(*value, "$", "$$", -1))
			}
		}
	}
	return yaml.Marshal(compose)
}
