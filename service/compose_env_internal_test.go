package service

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/IceWhaleTech/CasaOS-AppManagement/common"
	"github.com/IceWhaleTech/CasaOS-Common/utils/logger"
	"github.com/docker/compose/v2/pkg/api"
	"gotest.tools/v3/assert"
)

// a failed apply must put the .env back with docker-compose.yml, or disk and running state diverge
func TestPullAndApplyRestoresDotEnvOnFailure(t *testing.T) {
	logger.LogInitConsoleOnly()
	if runtime.GOOS == "windows" {
		// the restore path talks to the daemon pipe; go-winio keeps a completion goroutine for the
		// rest of the process and every goleak-checked test after this one would fail
		t.Skip("the docker named pipe leaks a goroutine that trips goleak in later tests")
	}
	if _, client, err := apiService(); err != nil {
		t.Skip("no docker cli:", err)
	} else {
		client.Close()
	}

	dir := t.TempDir()
	composeFile := filepath.Join(dir, common.ComposeYAMLFileName)
	const current = "name: casaos-env-test\nservices: {}\n"
	assert.NilError(t, os.WriteFile(composeFile, []byte(current), 0o600))
	a := &ComposeApp{Name: "casaos-env-test", WorkingDir: dir, ComposeFiles: []string{composeFile}}

	// fails after both files are written: `${NOPE}` cannot be cast to a number at load time
	broken := []byte("name: casaos-env-test\nservices:\n  a:\n    image: i\n    cpu_count: ${NOPE}\n")
	newEnv := []byte("NEW=1\n")

	assert.NilError(t, os.WriteFile(a.EnvFile(), []byte("OLD=1\n"), 0o600))
	assert.ErrorContains(t, a.pullAndApply(context.Background(), broken, &newEnv), "cpu_count")
	env, err := os.ReadFile(a.EnvFile())
	assert.NilError(t, err)
	assert.Equal(t, string(env), "OLD=1\n")
	compose, err := os.ReadFile(composeFile)
	assert.NilError(t, err)
	assert.Equal(t, string(compose), current)

	// no .env before: none after
	assert.NilError(t, os.Remove(a.EnvFile()))
	assert.ErrorContains(t, a.pullAndApply(context.Background(), broken, &newEnv), "cpu_count")
	_, err = os.Stat(a.EnvFile())
	assert.Assert(t, os.IsNotExist(err))

	// loads and pulls, then the daemon refuses the container: the app failed to start, both
	// files go back. Before, success was recorded before the start was examined.
	service, client, err := apiService()
	assert.NilError(t, err)
	defer client.Close()
	t.Cleanup(func() {
		_ = service.Down(context.Background(), a.Name, api.DownOptions{RemoveOrphans: true})
	})
	unstartable := []byte("name: casaos-env-test\nservices:\n  a:\n    image: alpine:3.20\n    cap_add:\n      - NOT_A_CAP\n")
	assert.NilError(t, os.WriteFile(a.EnvFile(), []byte("OLD=1\n"), 0o600))
	err = a.pullAndApply(context.Background(), unstartable, &newEnv)
	assert.Assert(t, err != nil, "an app the daemon refuses to create must fail the apply")
	assert.Assert(t, strings.Contains(strings.ToLower(err.Error()), "cap"), err.Error())
	env, err = os.ReadFile(a.EnvFile())
	assert.NilError(t, err)
	assert.Equal(t, string(env), "OLD=1\n")
	compose, err = os.ReadFile(composeFile)
	assert.NilError(t, err)
	assert.Equal(t, string(compose), current)
}

// an App Store update rewrites docker-compose.yml from the runtime project, whose `.env` is
// resolved: one update baked every secret, port and path into the file and left `.env` dead
func TestUpdateKeepsDotEnvReferencesInComposeFile(t *testing.T) {
	logger.LogInitConsoleOnly()
	dir := t.TempDir()
	composeFile := filepath.Join(dir, common.ComposeYAMLFileName)
	const compose = "name: wg-easy\nservices:\n  wg-easy:\n    image: ghcr.io/wg-easy/wg-easy:1\n    environment:\n      SECRET: ${SECRET}\n" +
		"    ports:\n      - ${PORT}:80\n    volumes:\n      - ${DATA}:/data\n      - ./config:/config\nx-casaos:\n  title:\n    en_us: wg-easy\n"
	assert.NilError(t, os.WriteFile(composeFile, []byte(compose), 0o600))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=s3cr3t\nPORT=8080\nDATA=/srv/d\n"), 0o600))

	a, err := LoadComposeAppFromConfigFile("wg-easy", composeFile) // the runtime project, as List serves it
	assert.NilError(t, err)
	assert.Equal(t, *a.Services["wg-easy"].Environment["SECRET"], "s3cr3t")
	store, err := NewComposeAppFromYAML([]byte("name: wg-easy\nservices:\n  wg-easy:\n    image: ghcr.io/wg-easy/wg-easy:2\n"), true, true)
	assert.NilError(t, err)

	out, err := a.updatedComposeYAML(store)
	assert.NilError(t, err)
	for _, want := range []string{"image: ghcr.io/wg-easy/wg-easy:2", "SECRET: ${SECRET}", "published: ${PORT}", "source: ${DATA}\n", "source: ./config\n"} {
		assert.Assert(t, strings.Contains(string(out), want), "%s missing in\n%s", want, out)
	}
	for _, leak := range []string{"s3cr3t", "8080", "/srv/d", dir} {
		assert.Assert(t, !strings.Contains(string(out), leak), "%s baked in\n%s", leak, out)
	}

	// and the runtime still resolves what the update wrote
	assert.NilError(t, os.WriteFile(composeFile, out, 0o600))
	running, err := LoadComposeAppFromConfigFile("wg-easy", composeFile)
	assert.NilError(t, err)
	service := running.Services["wg-easy"]
	assert.Equal(t, service.Image, "ghcr.io/wg-easy/wg-easy:2")
	assert.Equal(t, *service.Environment["SECRET"], "s3cr3t")
	assert.Equal(t, service.Ports[0].Published, "8080")
	assert.Assert(t, strings.HasSuffix(filepath.ToSlash(service.Volumes[0].Source), "/srv/d"), service.Volumes[0].Source)
	assert.Equal(t, service.Volumes[1].Source, filepath.Join(dir, "config"))

	// an unreadable .env is an error: an empty keep-set would bake every value
	assert.NilError(t, os.WriteFile(a.EnvFile(), []byte("KEY VALUE\n"), 0o600))
	_, err = a.updatedComposeYAML(store)
	assert.ErrorContains(t, err, "line 1")
}
