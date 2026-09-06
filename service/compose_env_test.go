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
	t.Setenv("CASAOS_TEST_FROM_OS", "os")
	id, composeFile := installedAppWithDotEnv(t, "SECRET=s3cr3t\nBARE=b\nTZ=Mars/Olympus\nCASAOS_TEST_FROM_OS=dotenv\n")
	keep, err := (&service.ComposeApp{WorkingDir: filepath.Dir(composeFile)}).EnvKeys()
	assert.NilError(t, err)
	assert.DeepEqual(t, keep, map[string]struct{}{"SECRET": {}, "BARE": {}}) // TZ and the OS env win at run time, not kept

	editing, err := service.LoadComposeAppForEditing(id, composeFile, keep)
	assert.NilError(t, err)
	get, err := service.GenerateYAMLFromComposeApp(*editing)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(get), "SECRET: ${SECRET}"), string(get))
	assert.Assert(t, !strings.Contains(string(get), "s3cr3t"), string(get))
	assert.Assert(t, strings.Contains(string(get), "PASSWORD_HASH: $$2a$$12$$zBIMbL5/"), string(get)) // still escaped exactly once
	assert.Assert(t, strings.Contains(string(get), "BARE: ${BARE}"), string(get))                     // bare key becomes an explicit reference
	assert.Assert(t, strings.Contains(string(get), "TZ: ${TZ}"), string(get))                         // a runtime reference, as the file has it

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

	// a `${VAR}` inside .env resolves as at run time: the base map, then the OS environment
	t.Setenv("CASAOS_TEST_FROM_OS", "os")
	env, err = service.ParseEnvFile([]byte("FROM_OS=${CASAOS_TEST_FROM_OS:?x}\nFROM_BASE=${PUID}\n"))
	assert.NilError(t, err)
	assert.Equal(t, env["FROM_OS"], "os")
	assert.Equal(t, env["FROM_BASE"], common.DefaultPUID)
	_, err = service.ParseEnvFile([]byte("X=${CASAOS_TEST_NOT_SET:?unset}\n"))
	assert.ErrorContains(t, err, "unset")

	// what the runtime defines itself is refused, not silently ignored
	for _, reserved := range []string{"TZ=Mars/Olympus\n", "AppID=x\n", "CASAOS_TEST_FROM_OS=dotenv\n"} {
		_, err := service.ParseEnvFile([]byte("OK=1\n" + reserved))
		assert.ErrorContains(t, err, "wins over .env", reserved)
	}
}

func TestWriteEnvFile(t *testing.T) {
	app := &service.ComposeApp{WorkingDir: t.TempDir()}

	text, err := app.EnvFileText()
	assert.NilError(t, err)
	assert.Equal(t, len(text), 0)
	keys, err := app.EnvKeys()
	assert.NilError(t, err)
	assert.Equal(t, len(keys), 0)

	assert.NilError(t, app.WriteEnvFile([]byte("A=1\nB=2\n")))
	text, err = app.EnvFileText()
	assert.NilError(t, err)
	assert.Equal(t, string(text), "A=1\nB=2\n")
	keys, err = app.EnvKeys()
	assert.NilError(t, err)
	assert.DeepEqual(t, keys, map[string]struct{}{"A": {}, "B": {}})

	// an unreadable .env is an error, not an empty keep-set that would bake every value
	assert.NilError(t, os.WriteFile(app.EnvFile(), []byte("KEY VALUE\n"), 0o600))
	_, err = app.EnvKeys()
	assert.ErrorContains(t, err, "line 1")

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
	keep, err := (&service.ComposeApp{WorkingDir: filepath.Dir(composeFile)}).EnvKeys()
	assert.NilError(t, err)

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

// `${DATA}:/data` is the second most common use of .env; compose takes a leading reference for a
// named volume and absolutises a kept `source` into `<app dir>/${DATA}`, so the editing load must
// move it to the long syntax and leave paths as written.
func TestSettingsRoundTripKeepsDotEnvReferenceInVolumes(t *testing.T) {
	logger.LogInitConsoleOnly()
	id, composeFile := installedApp(t,
		"name: wg-easy\nservices:\n  wg-easy:\n    image: ghcr.io/wg-easy/wg-easy\n    volumes:\n"+
			"      - ${DATA}:/data\n      - ${DATA}/sub:/data/sub:ro\n      - $DATA:/data/short\n"+
			"      - type: bind\n        source: ${DATA}\n        target: /data/long\n      - ./config:/config\n"+
			"x-casaos:\n  title:\n    en_us: wg-easy\n",
		"DATA=./real-data\n")
	dir := filepath.Dir(composeFile)
	keep, err := (&service.ComposeApp{WorkingDir: dir}).EnvKeys()
	assert.NilError(t, err)

	editing, err := service.LoadComposeAppForEditing(id, composeFile, keep)
	assert.NilError(t, err)
	get, err := service.GenerateYAMLFromComposeApp(*editing)
	assert.NilError(t, err)
	for _, want := range []string{"source: ${DATA}\n", "source: ${DATA}/sub\n", "source: $DATA\n", "target: /data/sub", "read_only: true",
		"target: /data/long", "source: ./config\n", "create_host_path: true"} {
		assert.Assert(t, strings.Contains(string(get), want), "%s missing in\n%s", want, get)
	}
	assert.Equal(t, strings.Count(string(get), "type: bind"), 5, string(get))
	for _, leak := range []string{"real-data", dir} { // no resolved value, no resolved path
		assert.Assert(t, !strings.Contains(string(get), leak), "%s resolved in\n%s", leak, get)
	}

	// PUT of what GET returned is a no-op, and the short syntax a user types is accepted
	save := func(yaml string) string {
		app, err := service.ComposeAppFromSettingsYAML([]byte(yaml), keep)
		assert.NilError(t, err)
		out, err := service.GenerateYAMLFromComposeApp(*app)
		assert.NilError(t, err)
		return string(out)
	}
	assert.Equal(t, save(string(get)), string(get))
	typed := save("name: wg-easy\nservices:\n  wg-easy:\n    image: ghcr.io/wg-easy/wg-easy\n    volumes:\n      - ${DATA}:/data\n      - ./config:/config\n")
	assert.Assert(t, strings.Contains(typed, "source: ${DATA}\n"), typed)
	assert.Assert(t, strings.Contains(typed, "source: ./config\n"), typed) // not the working directory of the parse

	// what docker runs resolves every source, before and after the round trip
	for _, text := range []string{"", string(get)} {
		if text != "" {
			assert.NilError(t, os.WriteFile(composeFile, []byte(text), 0o600))
		}
		running, err := service.LoadComposeAppFromConfigFile(id, composeFile)
		assert.NilError(t, err)
		sources := []string{}
		for _, volume := range running.Services[id].Volumes {
			sources = append(sources, volume.Source)
		}
		data := filepath.Join(dir, "real-data")
		assert.DeepEqual(t, sources, []string{data, filepath.Join(data, "sub"), data, data, filepath.Join(dir, "config")})
	}

	// a reference in the container path has no text form: an error, never a resolved value
	_, composeFile = installedApp(t, "name: wg-easy\nservices:\n  wg-easy:\n    image: i\n    volumes:\n      - /srv:${TARGET}\n", "TARGET=/data\n")
	_, err = service.LoadComposeAppForEditing(id, composeFile, map[string]struct{}{"TARGET": {}})
	assert.ErrorContains(t, err, "only the host path")
	assert.Assert(t, !strings.Contains(err.Error(), "/data"), err.Error())
}

// a field compose needs typed (a number, a boolean, a size) cannot keep a reference: the editing
// load fails naming the field and never shows the value, .env is not read there
func TestEditingLoadNamesTheFieldThatCannotKeepDotEnv(t *testing.T) {
	logger.LogInitConsoleOnly()
	keep := map[string]struct{}{"CPUS": {}}
	for _, field := range []string{"cpus: ${CPUS}", "cpu_count: $CPUS", "privileged: ${CPUS:-false}", "mem_limit: ${CPUS}"} {
		id, composeFile := installedApp(t, "name: wg-easy\nservices:\n  wg-easy:\n    image: i\n    "+field+"\n", "CPUS=1.5\n")
		_, err := service.LoadComposeAppForEditing(id, composeFile, keep)
		assert.ErrorContains(t, err, strings.SplitN(field, ":", 2)[0], field)
		assert.ErrorContains(t, err, "kept only in environment, env_file, ports and volumes", field)
		assert.ErrorContains(t, err, composeFile, field)
		assert.Assert(t, !strings.Contains(err.Error(), "1.5"), err.Error())
	}

	// env_file is free text: kept, as written, and the runtime resolves it
	id, composeFile := installedApp(t, "name: wg-easy\nservices:\n  wg-easy:\n    image: i\n    env_file: ${ENVF}\n    environment:\n      B: 2\n", "ENVF=./x.env\n")
	assert.NilError(t, os.WriteFile(filepath.Join(filepath.Dir(composeFile), "x.env"), []byte("A=1\n"), 0o600))
	keep = map[string]struct{}{"ENVF": {}}
	editing, err := service.LoadComposeAppForEditing(id, composeFile, keep)
	assert.NilError(t, err)
	get, err := service.GenerateYAMLFromComposeApp(*editing)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(get), "- ${ENVF}\n"), string(get))
	assert.Assert(t, !strings.Contains(string(get), "A:"), string(get)) // not merged into environment
	saved, err := service.ComposeAppFromSettingsYAML(get, keep)
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(composeFile, get, 0o600))
	running, err := service.LoadComposeAppFromConfigFile(id, composeFile)
	assert.NilError(t, err)
	assert.Equal(t, saved.Services[id].EnvFiles[0].Path, "${ENVF}")
	assert.Equal(t, *running.Services[id].Environment["A"], "1")
}

// a reference to a key the runtime defines (`$AppID` in a volume path, `${TZ}`, `$PUID`) survives
// a settings save as typed, for the runtime to resolve: the App Store idiom, which 0f45aaf broke by
// baking `$$AppID`. WEBUI_PORT is the exception, allocated at save time. And it survives every
// GET/PUT after that: the editing load kept only the .env keys, so the second save baked the
// literals the first one had kept.
func TestSettingsSaveKeepsRuntimeReferences(t *testing.T) {
	logger.LogInitConsoleOnly()
	keep := map[string]struct{}{"SECRET": {}}
	save := func(yaml string) string {
		app, err := service.ComposeAppFromSettingsYAML([]byte(yaml), keep)
		assert.NilError(t, err)
		out, err := service.GenerateYAMLFromComposeApp(*app)
		assert.NilError(t, err)
		return string(out)
	}

	spellings := []string{"TZ: ${TZ}", "PUID: $PUID", "GID: ${PGID}", "USER: ${DefaultUserName}", "APP: ${AppID}", "SECRET: ${SECRET}",
		"LITERAL_PUID: \"1000\"", "LITERAL_APP: /DATA/AppData/wg-easy"}
	saved := save("name: wg-easy\nservices:\n  wg-easy:\n    image: ghcr.io/wg-easy/wg-easy\n    volumes:\n      - /DATA/AppData/$AppID/config:/config\n" +
		"    environment:\n      " + strings.Join(spellings, "\n      ") + "\n      PORT: ${WEBUI_PORT}\nx-casaos:\n  title:\n    en_us: wg-easy\n")
	for _, spelling := range append(spellings, "source: /DATA/AppData/$AppID/config") {
		assert.Assert(t, strings.Contains(saved, spelling), "%s lost in\n%s", spelling, saved)
	}
	assert.Assert(t, !strings.Contains(saved, "$$"), saved)
	assert.Assert(t, !strings.Contains(saved, "WEBUI_PORT"), saved) // baked to a free port
	assert.Equal(t, save(saved), saved)

	// and the runtime resolves what was typed
	id, composeFile := installedApp(t, saved, "SECRET=s3cr3t\n")
	resolves := func() {
		t.Helper()
		running, err := service.LoadComposeAppFromConfigFile(id, composeFile)
		assert.NilError(t, err)
		assert.Equal(t, running.Services[id].Volumes[0].Source, "/DATA/AppData/wg-easy/config")
		env := running.Services[id].Environment
		assert.Equal(t, *env["PUID"], common.DefaultPUID)
		assert.Equal(t, *env["GID"], common.DefaultPGID)
		assert.Equal(t, *env["USER"], common.DefaultUserName)
		assert.Equal(t, *env["APP"], "wg-easy")
		assert.Equal(t, *env["SECRET"], "s3cr3t")
		assert.Assert(t, *env["TZ"] != "${TZ}")
	}
	resolves()

	// GET shows the file as it is, a reference where it has one and a literal where it has one;
	// PUT of that changes nothing on disk, and GET of that is the same again
	get := func() string {
		t.Helper()
		editing, err := service.LoadComposeAppForEditing(id, composeFile, keep)
		assert.NilError(t, err)
		out, err := service.GenerateYAMLFromComposeApp(*editing)
		assert.NilError(t, err)
		return string(out)
	}
	assert.Equal(t, get(), saved)
	assert.Equal(t, save(get()), saved)
	assert.NilError(t, os.WriteFile(composeFile, []byte(save(get())), 0o600))
	assert.Equal(t, get(), saved)
	resolves()
}

// a kept reference in the default of another one (`${OTHER:-$SECRET}`) was resolved to the
// literal `${SECRET}`: GET showed `$${SECRET}` and the app lost the value after a save
func TestSettingsRoundTripKeepsNestedDotEnvReference(t *testing.T) {
	logger.LogInitConsoleOnly()
	t.Setenv("CASAOS_TEST_FROM_OS", "os")
	id, composeFile := installedApp(t,
		"name: wg-easy\nservices:\n  wg-easy:\n    image: ghcr.io/wg-easy/wg-easy\n    environment:\n"+
			"      NESTED: ${OTHER:-$SECRET}\n      BRACED: ${OTHER:-${SECRET}}\n      BOTH: ${SECRET2:-$SECRET}\n"+
			"      TAKEN: ${CASAOS_TEST_FROM_OS:-$SECRET} $SECRET2\nx-casaos:\n  title:\n    en_us: wg-easy\n",
		"SECRET=s3cr3t\nSECRET2=other\n")
	keep, err := (&service.ComposeApp{WorkingDir: filepath.Dir(composeFile)}).EnvKeys()
	assert.NilError(t, err)

	editing, err := service.LoadComposeAppForEditing(id, composeFile, keep)
	assert.NilError(t, err)
	get, err := service.GenerateYAMLFromComposeApp(*editing)
	assert.NilError(t, err)
	for _, want := range []string{"NESTED: $SECRET\n", "BRACED: ${SECRET}\n", "BOTH: ${SECRET2:-$SECRET}\n", "TAKEN: os $SECRET2\n"} {
		assert.Assert(t, strings.Contains(string(get), want), "%s missing in\n%s", want, get)
	}
	assert.Assert(t, !strings.Contains(string(get), "s3cr3t"), string(get))

	saved, err := service.ComposeAppFromSettingsYAML(get, keep)
	assert.NilError(t, err)
	out, err := service.GenerateYAMLFromComposeApp(*saved)
	assert.NilError(t, err)
	assert.Equal(t, string(out), string(get))

	assert.NilError(t, os.WriteFile(composeFile, out, 0o600))
	running, err := service.LoadComposeAppFromConfigFile(id, composeFile)
	assert.NilError(t, err)
	env := running.Services[id].Environment
	assert.Equal(t, *env["NESTED"], "s3cr3t")
	assert.Equal(t, *env["BRACED"], "s3cr3t")
	assert.Equal(t, *env["BOTH"], "other")
	assert.Equal(t, *env["TAKEN"], "os other")
}
