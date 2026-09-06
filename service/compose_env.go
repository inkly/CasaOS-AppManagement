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

// ParseEnvFile validates `.env` text with the parser compose itself uses at load time, so what
// passes here is exactly what the app will get.
func ParseEnvFile(text []byte) (map[string]string, error) {
	return dotenv.Parse(bytes.NewReader(text))
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
// `.env` edit would be a no-op. A missing or unparsable file yields an empty set.
func (a *ComposeApp) EnvKeys() map[string]struct{} {
	keys := map[string]struct{}{}
	env, err := dotenv.Read(a.EnvFile())
	if err != nil {
		return keys
	}
	for k := range env {
		keys[k] = struct{}{}
	}
	return keys
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
			resolve := o.Interpolate.LookupValue
			o.Interpolate.LookupValue = func(key string) (string, bool) {
				if _, ok := keep[key]; ok {
					return "${" + key + "}", true
				}
				return resolve(key)
			}
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

var keptRef = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

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
	if !strings.Contains(value, "${") {
		return value, nil
	}

	var refs []string
	resolved := keptRef.ReplaceAllStringFunc(value, func(ref string) string {
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
