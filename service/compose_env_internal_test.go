package service

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/IceWhaleTech/CasaOS-AppManagement/common"
	"github.com/IceWhaleTech/CasaOS-Common/utils/logger"
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
}
