package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IceWhaleTech/CasaOS-AppManagement/common"
	"github.com/IceWhaleTech/CasaOS-AppManagement/service"
	"github.com/IceWhaleTech/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

// on disk as the settings pipeline writes it: every literal `$` escaped once (#1988)
const hash = "$$2a$$12$$zBIMbL5/axcluAruNwKziuRCvJCVeh0xrKB1wT7qEoOH95g2L2p4G"

// an installed app: docker-compose.yml next to its .env
func installedApp(t *testing.T, compose, env string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	composeFile := filepath.Join(dir, common.ComposeYAMLFileName)
	assert.NilError(t, os.WriteFile(composeFile, []byte(compose), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600))
	return "wg-easy", composeFile
}

// docker-compose.yml referencing keys of the .env next to it
func installedAppWithDotEnv(t *testing.T, env string) (string, string) {
	t.Helper()
	return installedApp(t,
		"name: wg-easy\nservices:\n  wg-easy:\n    image: ghcr.io/wg-easy/wg-easy\n    environment:\n"+
			"      SECRET: ${SECRET}\n      TZ: ${TZ}\n      BARE:\n      PASSWORD_HASH: "+hash+"\n"+
			"x-casaos:\n  title:\n    en_us: wg-easy\n", env)
}

func TestDotEnvIsHonouredForInstalledApp(t *testing.T) {
	logger.LogInitConsoleOnly()
	id, composeFile := installedAppWithDotEnv(t, "SECRET=s3cr3t\nTZ=Mars/Olympus\n")

	app, err := service.LoadComposeAppFromConfigFile(id, composeFile)
	assert.NilError(t, err)

	env := app.Services[id].Environment
	assert.Equal(t, *env["SECRET"], "s3cr3t")
	assert.Assert(t, *env["TZ"] != "Mars/Olympus", "the base map (TZ, PUID...) and the OS env win over .env")
}

// GET must show `${SECRET}`, not its value, and PUT of what GET returned must be a no-op:
// otherwise the first settings save bakes the secret into docker-compose.yml and cuts the app
// off from its .env.
func TestSettingsRoundTripKeepsDotEnvReferences(t *testing.T) {
	logger.LogInitConsoleOnly()
	id, composeFile := installedAppWithDotEnv(t, "SECRET=s3cr3t\nBARE=b\n")
	keep := (&service.ComposeApp{WorkingDir: filepath.Dir(composeFile)}).EnvKeys()
	assert.DeepEqual(t, keep, map[string]struct{}{"SECRET": {}, "BARE": {}})

	editing, err := service.LoadComposeAppForEditing(id, composeFile, keep)
	assert.NilError(t, err)
	get, err := service.GenerateYAMLFromComposeApp(*editing)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(get), "SECRET: ${SECRET}"), string(get))
	assert.Assert(t, !strings.Contains(string(get), "s3cr3t"), string(get))
	assert.Assert(t, strings.Contains(string(get), "PASSWORD_HASH: $$2a$$12$$zBIMbL5/"), string(get)) // still escaped exactly once
	assert.Assert(t, strings.Contains(string(get), "BARE: ${BARE}"), string(get))                     // bare key becomes an explicit reference

	save := func(yaml string) string {
		app, err := service.ComposeAppFromSettingsYAML([]byte(yaml), keep)
		assert.NilError(t, err)
		out, err := service.GenerateYAMLFromComposeApp(*app)
		assert.NilError(t, err)
		return string(out)
	}
	assert.Equal(t, save(string(get)), string(get))

	// what the user types: a reference to a .env key stays one, anything else is a literal
	typed := save("name: wg-easy\nservices:\n  wg-easy:\n    image: ghcr.io/wg-easy/wg-easy\n    environment:\n" +
		"      SECRET: $SECRET\n      OTHER: ${UNKNOWN} $SECRET\nx-casaos:\n  title:\n    en_us: wg-easy\n")
	assert.Assert(t, strings.Contains(typed, "SECRET: $SECRET"), typed)
	assert.Assert(t, strings.Contains(typed, "OTHER: $$UNKNOWN $SECRET"), typed)
}

func TestParseEnvFile(t *testing.T) {
	env, err := service.ParseEnvFile([]byte("# comment\nKEY=v\nexport QUOTED=\"q v\"\nLITERAL='$KEY'\n"))
	assert.NilError(t, err)
	assert.DeepEqual(t, env, map[string]string{"KEY": "v", "QUOTED": "q v", "LITERAL": "$KEY"})

	for _, bad := range []string{"KEY VALUE\n", "KEY=\"unterminated\n"} {
		_, err := service.ParseEnvFile([]byte(bad))
		assert.ErrorContains(t, err, "line ", bad) // compose's own line-numbered message
	}
}

func TestWriteEnvFile(t *testing.T) {
	app := &service.ComposeApp{WorkingDir: t.TempDir()}

	text, err := app.EnvFileText()
	assert.NilError(t, err)
	assert.Equal(t, len(text), 0)
	assert.Equal(t, len(app.EnvKeys()), 0)

	assert.NilError(t, app.WriteEnvFile([]byte("A=1\nB=2\n")))
	text, err = app.EnvFileText()
	assert.NilError(t, err)
	assert.Equal(t, string(text), "A=1\nB=2\n")
	assert.DeepEqual(t, app.EnvKeys(), map[string]struct{}{"A": {}, "B": {}})

	assert.NilError(t, app.WriteEnvFile([]byte(" \n")))
	_, err = os.Stat(app.EnvFile())
	assert.Assert(t, os.IsNotExist(err))
	assert.NilError(t, app.WriteEnvFile(nil)) // removing twice is fine
}

// `${PORT}:80` is the most common use of .env; compose refuses `${PORT}` as a host port, so the
// editing load must move it to the long syntax rather than serve the resolved 8080.
func TestSettingsRoundTripKeepsDotEnvReferenceInPorts(t *testing.T) {
	logger.LogInitConsoleOnly()
	id, composeFile := installedApp(t,
		"name: wg-easy\nservices:\n  wg-easy:\n    image: ghcr.io/wg-easy/wg-easy\n    ports:\n"+
			"      - ${PORT}:80\n      - 127.0.0.1:$PORT:81/udp\n      - 9000:90\n    environment:\n      SECRET: ${SECRET}\n"+
			"x-casaos:\n  title:\n    en_us: wg-easy\n",
		"SECRET=s3cr3t\nPORT=8080\n")
	keep := (&service.ComposeApp{WorkingDir: filepath.Dir(composeFile)}).EnvKeys()

	editing, err := service.LoadComposeAppForEditing(id, composeFile, keep)
	assert.NilError(t, err)
	get, err := service.GenerateYAMLFromComposeApp(*editing)
	assert.NilError(t, err)
	for _, want := range []string{"published: ${PORT}", "published: $PORT", "target: 80", "target: 81", "protocol: udp", "host_ip: 127.0.0.1", "published: \"9000\"", "SECRET: ${SECRET}"} {
		assert.Assert(t, strings.Contains(string(get), want), "%s missing in\n%s", want, get)
	}
	for _, leak := range []string{"8080", "s3cr3t"} {
		assert.Assert(t, !strings.Contains(string(get), leak), "%s resolved in\n%s", leak, get)
	}

	// the settings parse takes both what GET returned and the short syntax a user types
	save := func(yaml string) string {
		app, err := service.ComposeAppFromSettingsYAML([]byte(yaml), keep)
		assert.NilError(t, err)
		out, err := service.GenerateYAMLFromComposeApp(*app)
		assert.NilError(t, err)
		return string(out)
	}
	assert.Equal(t, save(string(get)), string(get))
	typed := save("name: wg-easy\nservices:\n  wg-easy:\n    image: ghcr.io/wg-easy/wg-easy\n    ports:\n      - ${PORT}:80\n")
	assert.Assert(t, strings.Contains(typed, "published: ${PORT}"), typed)

	// what docker runs still resolves the port
	running, err := service.LoadComposeAppFromConfigFile(id, composeFile)
	assert.NilError(t, err)
	assert.Equal(t, running.Services[id].Ports[0].Published, "8080")

	// a reference where compose needs a number has no text form: an error, never a resolved value
	_, composeFile = installedApp(t, "name: wg-easy\nservices:\n  wg-easy:\n    image: i\n    ports:\n      - 80:${PORT}\n", "PORT=8080\n")
	_, err = service.LoadComposeAppForEditing(id, composeFile, keep)
	assert.ErrorContains(t, err, "only the host side")
}

// the user's spelling of a kept reference survives the round trip: interpolation would collapse
// `$$SECRET` (a literal) to `${SECRET}` and drop the modifier of `${SECRET:-dflt}`.
func TestSettingsRoundTripKeepsDotEnvSpelling(t *testing.T) {
	logger.LogInitConsoleOnly()
	keep := map[string]struct{}{"SECRET": {}}
	save := func(yaml string) string {
		app, err := service.ComposeAppFromSettingsYAML([]byte(yaml), keep)
		assert.NilError(t, err)
		out, err := service.GenerateYAMLFromComposeApp(*app)
		assert.NilError(t, err)
		return string(out)
	}

	spellings := []string{"REF: ${SECRET}", "SHORT: $SECRET", "DFLT: ${SECRET:-dflt}", "REQ: ${SECRET:?req}", "LITERAL: $$SECRET",
		"MIXED: $$SECRET is not ${SECRET}", "OTHER: $$OTHER $$UNKNOWN"}
	typed := "name: wg-easy\nservices:\n  wg-easy:\n    image: ghcr.io/wg-easy/wg-easy\n    environment:\n      " +
		strings.Join(spellings, "\n      ") + "\nx-casaos:\n  title:\n    en_us: wg-easy\n"
	saved := save(typed)
	for _, spelling := range spellings {
		assert.Assert(t, strings.Contains(saved, spelling), "%s lost in\n%s", spelling, saved)
	}
	assert.Equal(t, save(saved), saved)

	// and docker reads what was typed
	id, composeFile := installedApp(t, saved, "SECRET=s3cr3t\n")
	running, err := service.LoadComposeAppFromConfigFile(id, composeFile)
	assert.NilError(t, err)
	env := running.Services[id].Environment
	assert.Equal(t, *env["REF"], "s3cr3t")
	assert.Equal(t, *env["DFLT"], "s3cr3t")
	assert.Equal(t, *env["REQ"], "s3cr3t")
	assert.Equal(t, *env["LITERAL"], "$SECRET")
	assert.Equal(t, *env["MIXED"], "$SECRET is not s3cr3t")

	// what GET shows for the same file
	editing, err := service.LoadComposeAppForEditing(id, composeFile, keep)
	assert.NilError(t, err)
	get, err := service.GenerateYAMLFromComposeApp(*editing)
	assert.NilError(t, err)
	assert.Equal(t, string(get), saved)
}
