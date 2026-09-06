package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IceWhaleTech/CasaOS-Common/utils/file"
	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/loader"
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
