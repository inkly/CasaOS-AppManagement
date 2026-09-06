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
// every save (#1988). keep is ComposeApp.EnvKeys(): `${KEY}` references to the app's `.env` are
// left as references instead of being resolved.
func ComposeAppFromSettingsYAML(buf []byte, keep map[string]struct{}) (*ComposeApp, error) {
	return newComposeAppFromYAML(buf, false, true, keep)
}

// dollarToken splits an env value into the pieces `$` escaping cares about: `$$`, `${NAME}`,
// `$NAME` and a lone `$`.
var dollarToken = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)|\$`)

// escapeDollars doubles every `$` of an env value (what compose reads back as a literal `$`) except
// references to a key in keep, emitted as `${KEY}` for compose to resolve from `.env`.
// ponytail: a literal `$NAME` where NAME happens to be a `.env` key is kept as a reference too, the
// same ambiguity compose itself has; users write `$$NAME` for the literal.
func escapeDollars(value string, keep map[string]struct{}) string {
	return dollarToken.ReplaceAllStringFunc(value, func(token string) string {
		m := dollarToken.FindStringSubmatch(token)
		if name := m[1] + m[2]; name != "" {
			if _, ok := keep[name]; ok {
				return "${" + name + "}"
			}
		}
		return strings.ReplaceAll(token, "$", "$$")
	})
}

func GenerateYAMLFromComposeApp(compose ComposeApp, keep map[string]struct{}) ([]byte, error) {
	// to duplicate Specify Chars
	for _, service := range compose.Services {
		// it should duplicate all values that contains $. But for now, we only duplicate the env values
		for key, value := range service.Environment {
			if value != nil && strings.ContainsAny(*value, "$") {
				service.Environment[key] = utils.Ptr(escapeDollars(*value, keep))
			}
		}
	}
	return yaml.Marshal(compose)
}
