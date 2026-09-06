package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/IceWhaleTech/CasaOS-Common/utils/file"
	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/interpolation"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/tree"
	"github.com/compose-spec/compose-go/v2/types"
)

// EnvFile is the `.env` docker compose reads next to the compose file of an installed app.
func (a *ComposeApp) EnvFile() string {
	return filepath.Join(a.WorkingDir, ".env")
}

// EnvFileText returns the raw `.env`, empty when there is none.
func (a *ComposeApp) EnvFileText() ([]byte, error) {
	text, err := os.ReadFile(a.EnvFile())
	if os.IsNotExist(err) {
		return nil, nil
	}
	return text, err
}

// reservedEnvKey says whether the runtime defines key itself: AppID, the base interpolation map
// (TZ, PUID, PGID...) and the environment of this process all win over `.env` when docker loads
// the app (LoadComposeAppFromConfigFile), so a `.env` definition of one is dead text.
func reservedEnvKey(key string) bool {
	if key == "AppID" {
		return true
	}
	if _, ok := baseInterpolationMap()[key]; ok {
		return true
	}
	_, ok := os.LookupEnv(key)
	return ok
}

// runtimeEnvLookup resolves a `${VAR}` inside `.env` the way LoadComposeAppFromConfigFile does:
// the base interpolation map, then the environment of this process.
// ponytail: AppID is not known here, a `.env` referencing `${AppID}` is refused although it runs.
func runtimeEnvLookup(key string) (string, bool) {
	if value, ok := baseInterpolationMap()[key]; ok {
		return value, true
	}
	return os.LookupEnv(key)
}

// parseEnv reads `.env` text with the parser and the lookup compose itself uses at load time, so
// what passes here is exactly what the app will get.
func parseEnv(text []byte) (map[string]string, error) {
	return dotenv.ParseWithLookup(bytes.NewReader(text), runtimeEnvLookup)
}

// ParseEnvFile validates `.env` text as the runtime will read it; a reserved key is refused
// rather than ignored.
func ParseEnvFile(text []byte) (map[string]string, error) {
	env, err := parseEnv(text)
	if err != nil {
		return nil, err
	}
	for key := range env {
		if reservedEnvKey(key) {
			return nil, fmt.Errorf("%s is set by CasaOS and wins over .env, remove it from the file", key)
		}
	}
	return env, nil
}

// WriteEnvFile replaces the `.env`; an empty body removes it.
func (a *ComposeApp) WriteEnvFile(text []byte) error {
	if len(bytes.TrimSpace(text)) == 0 {
		if err := os.Remove(a.EnvFile()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return file.WriteToFullPath(text, a.EnvFile(), 0o600)
}

// EnvKeys is the keep-set of the settings round trip: the names defined in the app's `.env`.
// Values of those stay `${KEY}` references in docker-compose.yml instead of being baked in,
// otherwise the first settings save would copy the secrets into the compose file and every later
// `.env` edit would be a no-op. A reserved key (see reservedEnvKey) is left out: the editor then
// shows the value the runtime uses. A missing file yields an empty set; an unreadable one is an
// error, an empty set would bake every value on the next save.
func (a *ComposeApp) EnvKeys() (map[string]struct{}, error) {
	text, err := a.EnvFileText()
	if err != nil {
		return nil, err
	}
	env, err := parseEnv(text)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", a.EnvFile(), err)
	}
	keys := map[string]struct{}{}
	for k := range env {
		if !reservedEnvKey(k) {
			keys[k] = struct{}{}
		}
	}
	return keys, nil
}

// keepRefs is the environment the settings pipeline resolves a kept key from: `${KEY}` itself, so
// a bare `KEY` entry becomes an explicit reference instead of a baked value (or nothing).
func keepRefs(keep map[string]struct{}) map[string]string {
	refs := map[string]string{}
	for k := range keep {
		refs[k] = "${" + k + "}"
	}
	return refs
}

// LoadComposeAppForEditing loads an installed app for the settings editor: `${KEY}` references
// and bare `KEY` entries for a key in keep come out as `${KEY}`, and `.env` itself is never read.
// LoadComposeAppFromConfigFile (what docker runs) keeps resolving everything.
func LoadComposeAppForEditing(appID, configFile string, keep map[string]struct{}) (*ComposeApp, error) {
	env := []string{fmt.Sprintf("%s=%s", "AppID", appID)}
	for k, v := range baseInterpolationMap() {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	for k, v := range keepRefs(keep) {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	options, err := cli.NewProjectOptions(
		[]string{configFile},
		cli.WithWorkingDirectory(filepath.Dir(configFile)),
		cli.WithOsEnv,
		cli.WithEnv(env),
		cli.WithName(appID),
		cli.WithLoadOptions(func(o *loader.Options) {
			o.SkipValidation = true
			keepPortRefs(o)
			o.Interpolate.Substitute = substituteEscaping(keep)
		}),
	)
	if err != nil {
		return nil, err
	}

	project, err := options.LoadProject(context.Background())
	if err != nil {
		return nil, err
	}

	return (*ComposeApp)(project), nil
}

// keptToken splits compose text into what the settings pipeline copies verbatim: `$$` (an escaped
// dollar, so the name after it is not a reference), `${NAME...}` with any modifier, `$NAME`.
var keptToken = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)[^}]*\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// substituteEscaping interpolates for the settings pipeline, whose output is compose text again:
// every `$` of a resolved value is escaped as `$$`, so GenerateYAMLFromComposeApp writes strings
// as they are, and a reference to a key of keep comes back exactly as typed (`$SECRET`,
// `${SECRET:-dflt}`, `${SECRET:?req}`, the literal `$$SECRET`) instead of collapsing to `${SECRET}`.
func substituteEscaping(keep map[string]struct{}) func(string, template.Mapping) (string, error) {
	const mark = "\x00" // never in YAML text
	return func(text string, mapping template.Mapping) (string, error) {
		var kept []string
		protected := keptToken.ReplaceAllStringFunc(text, func(token string) string {
			m := keptToken.FindStringSubmatch(token)
			if _, ok := keep[m[1]+m[2]]; !ok {
				return token
			}
			kept = append(kept, token)
			return mark
		})

		resolved, err := template.Substitute(protected, mapping)
		if err != nil {
			return "", err
		}

		resolved = strings.ReplaceAll(resolved, "$", "$$")
		for _, token := range kept {
			resolved = strings.Replace(resolved, mark, token, 1)
		}
		return resolved, nil
	}
}

// keepPortRefs registers keptPortSyntax on `services.*.ports.*`.
func keepPortRefs(o *loader.Options) {
	casts := map[tree.Path]interpolation.Cast{}
	for path, cast := range o.Interpolate.TypeCastMapping { // the default mapping is shared, copy it
		casts[path] = cast
	}
	casts[tree.NewPath("services", tree.PathMatchAll, "ports", tree.PathMatchList)] = keptPortSyntax
	o.Interpolate.TypeCastMapping = casts
}

// keptPortSyntax runs on each port right after interpolation, where compose parses the short
// syntax and refuses `${PORT}:80` ("invalid hostPort"). A kept reference is moved to the long
// syntax, where `published` is free text docker resolves from `.env` at run time: the reference
// is swapped for a port number to parse, then put back.
// ponytail: a reference in the container port (`80:${TARGET}`) has no text form, it is an error.
func keptPortSyntax(value string) (any, error) {
	if !strings.Contains(value, "$") {
		return value, nil
	}

	var refs []string
	resolved := keptToken.ReplaceAllStringFunc(value, func(ref string) string {
		refs = append(refs, ref)
		return strconv.Itoa(65000 + len(refs))
	})

	parsed, err := types.ParsePortConfig(resolved)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", value, err)
	}
	if len(parsed) != 1 {
		return nil, fmt.Errorf("%s: a port range cannot keep a .env reference", value)
	}

	port := parsed[0]
	restored := 0
	for i, ref := range refs {
		placeholder := strconv.Itoa(65001 + i)
		for _, field := range []*string{&port.Published, &port.HostIP} {
			if strings.Contains(*field, placeholder) {
				*field = strings.ReplaceAll(*field, placeholder, ref)
				restored++
			}
		}
	}
	if restored != len(refs) {
		return nil, fmt.Errorf("%s: only the host side of a port can reference .env", value)
	}

	long := map[string]any{"target": port.Target, "published": port.Published, "protocol": port.Protocol, "mode": port.Mode}
	if port.HostIP != "" {
		long["host_ip"] = port.HostIP
	}
	return long, nil
}
