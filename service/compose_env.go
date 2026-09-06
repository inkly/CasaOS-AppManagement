package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/IceWhaleTech/CasaOS-Common/utils/file"
	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/format"
	"github.com/compose-spec/compose-go/v2/interpolation"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/tree"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/mitchellh/mapstructure"
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

// settingsKeep is the keep-set of the settings parse: the `.env` keys plus the keys the runtime
// defines (AppID, TZ, PUID...), so `$AppID` typed in a volume path stays for the runtime to resolve
// (the App Store idiom) instead of being baked as the literal `$$AppID`. WEBUI_PORT is not one of
// them: the settings parse allocates it on purpose (newComposeAppFromYAML). The editing load uses
// the same set, so a runtime reference survives any number of GET/PUT round trips: keeping it on
// the save only, the next GET baked `/DATA/AppData/wg-easy/config`, `PUID: "1000"` and the zone of
// the moment into the editor, and the save after that into the file.
func settingsKeep(keep map[string]struct{}) map[string]struct{} {
	all := map[string]struct{}{"AppID": {}}
	for k := range keep {
		all[k] = struct{}{}
	}
	for k := range baseInterpolationMap() {
		all[k] = struct{}{}
	}
	return all
}

// LoadComposeAppForEditing loads an installed app for the settings editor: a reference to a key
// of keep or to a key the runtime defines (settingsKeep) comes out as written, a bare `KEY` entry
// for one of them as `${KEY}`, and `.env` itself is never read. LoadComposeAppFromConfigFile (what
// docker runs) keeps resolving everything.
func LoadComposeAppForEditing(appID, configFile string, keep map[string]struct{}) (*ComposeApp, error) {
	keep = settingsKeep(keep)
	env := []string{}
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
			keepPathRefs(o)
			keepRefCasts(o)
			o.Interpolate.Substitute = substituteEscaping(keep)
		}),
	)
	if err != nil {
		return nil, err
	}

	project, err := options.LoadProject(context.Background())
	if err != nil {
		// the loader names the field (`services.a.cpus`) and, since `.env` is never read here, shows
		// the reference itself, never its value
		return nil, fmt.Errorf("%s: %w; a .env reference is kept only in %s, for any other field edit the file on disk", configFile, err, keptFields)
	}

	return (*ComposeApp)(project), nil
}

// keptFields is where a `.env` reference survives the settings round trip: free-text fields, and
// the two casts below. Anywhere compose needs a number, a boolean or a size, the reference cannot
// be kept and the editing load fails instead of serving the resolved value.
const keptFields = "environment, env_file, ports and volumes"

// keptToken splits compose text into what the settings pipeline copies verbatim: `$$` (an escaped
// dollar, so the name after it is not a reference), `${NAME...}` with any modifier, `$NAME`.
var keptToken = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)[^}]*\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// substituteEscaping interpolates for the settings pipeline, whose output is compose text again:
// every `$` of a resolved value is escaped as `$$`, so GenerateYAMLFromComposeApp writes strings
// as they are, and a reference to a key of keep comes back exactly as typed (`$SECRET`,
// `${SECRET:-dflt}`, `${SECRET:?req}`, the literal `$$SECRET`) instead of collapsing to `${SECRET}`.
// A token that is not kept but carries a kept reference in its text (`${OTHER:-$SECRET}`,
// `${OTHER:?need $SECRET}`) is kept verbatim as a whole: the runtime evaluates it the same way,
// and OTHER applies if it shows up in `.env` or the OS environment later. The marks are numbered,
// so a mark is never confused with the text around it.
func substituteEscaping(keep map[string]struct{}) func(string, template.Mapping) (string, error) {
	const mark = "\x00" // never in YAML text
	var isKept func(string) bool
	isKept = func(token string) bool {
		m := keptToken.FindStringSubmatch(token)
		if _, ok := keep[m[1]+m[2]]; ok {
			return true
		}
		if m[1] == "" {
			return false // `$$` or `$NAME`: nothing nested
		}
		for _, nested := range keptToken.FindAllString(token[len("${"+m[1]):], -1) {
			if isKept(nested) {
				return true
			}
		}
		return false
	}
	return func(text string, mapping template.Mapping) (string, error) {
		var kept []string
		protected := keptToken.ReplaceAllStringFunc(text, func(token string) string {
			if !isKept(token) {
				return token
			}
			kept = append(kept, token)
			return mark + strconv.Itoa(len(kept)-1) + mark
		})

		resolved, err := template.Substitute(protected, mapping)
		if err != nil {
			return "", err
		}

		resolved = strings.ReplaceAll(resolved, "$", "$$")
		for i, token := range kept {
			resolved = strings.ReplaceAll(resolved, mark+strconv.Itoa(i)+mark, token)
		}
		return resolved, nil
	}
}

// keepPathRefs leaves every path as written in the file: compose would otherwise absolutise a kept
// `source: ${DATA}` into `<app dir>/${DATA}` (and a settings save bakes the absolute path of the
// moment into docker-compose.yml). So a relative `./config` comes out as `./config`, not as the
// `<app dir>/config` the runtime computes; the runtime (LoadComposeAppFromConfigFile) still resolves
// it. env_file is then read relative to the process, not the app: it is left unread, `environment`
// shows what the file says instead of the merge with env_file the runtime computes.
func keepPathRefs(o *loader.Options) {
	o.ResolvePaths = false
	o.SkipResolveEnvironment = true
}

// keepRefCasts registers keptPortSyntax on `services.*.ports.*` and keptVolumeSyntax on
// `services.*.volumes.*`.
func keepRefCasts(o *loader.Options) {
	casts := map[tree.Path]interpolation.Cast{}
	for path, cast := range o.Interpolate.TypeCastMapping { // the default mapping is shared, copy it
		casts[path] = cast
	}
	casts[tree.NewPath("services", tree.PathMatchAll, "ports", tree.PathMatchList)] = keptPortSyntax
	casts[tree.NewPath("services", tree.PathMatchAll, "volumes", tree.PathMatchList)] = keptVolumeSyntax
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

// keptVolumeSyntax runs on each volume right after interpolation, where compose parses the short
// syntax and takes a leading `${DATA}` for a named volume ("refers to undefined volume"). The
// reference is swapped for an absolute path to parse, then put back in the long syntax, where
// `source` is free text docker resolves from `.env` at run time.
// ponytail: a leading reference is declared a bind mount, the common case; `.env` naming a named
// volume there is not supported. A reference in the container path or the options is an error.
func keptVolumeSyntax(value string) (any, error) {
	if !strings.Contains(value, "$") {
		return value, nil
	}
	const mark = "/\x01" // an absolute path, never in YAML text (NUL ends a spec for ParseVolume)

	var refs []string
	swapped := keptToken.ReplaceAllStringFunc(value, func(ref string) string {
		refs = append(refs, ref)
		return mark
	})

	volume, err := format.ParseVolume(swapped)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", value, err)
	}
	if strings.Count(volume.Source, mark) != len(refs) {
		return nil, fmt.Errorf("%s: only the host path of a volume can reference .env", value)
	}
	for _, ref := range refs {
		volume.Source = strings.Replace(volume.Source, mark, ref, 1)
	}
	volume.Target = path.Clean(volume.Target)

	long := map[string]any{} // as compose encodes the short syntax
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{Result: &long, TagName: "yaml"})
	if err != nil {
		return nil, err
	}
	return long, decoder.Decode(volume)
}
