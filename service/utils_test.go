package service_test

import (
	"strings"
	"testing"

	"github.com/IceWhaleTech/CasaOS-AppManagement/service"
	"github.com/IceWhaleTech/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

// #1988 - re-saving the YAML the settings page got back must not double `$` in env values.
func TestSettingsYAMLRoundTripIsIdempotent(t *testing.T) {
	logger.LogInitConsoleOnly()

	const hash = "$2a$12$zBIMbL5/axcluAruNwKziuRCvJCVeh0xrKB1wT7qEoOH95g2L2p4G"
	const composeYAML = "name: wg-easy\nservices:\n  wg-easy:\n    image: ghcr.io/wg-easy/wg-easy\n    environment:\n      PASSWORD_HASH: " + hash + "\n"

	save := func(yaml []byte) []byte {
		// same two calls as ApplyComposeAppSettings in route/v2/compose_app.go
		app, err := service.ComposeAppFromSettingsYAML(yaml, nil)
		assert.NilError(t, err)
		out, err := service.GenerateYAMLFromComposeApp(*app, nil)
		assert.NilError(t, err)
		return out
	}

	first := save([]byte(composeYAML))
	assert.Assert(t, strings.Contains(string(first), "PASSWORD_HASH: $$2a$$12$$zBIMbL5/"), string(first)) // raw `$` escaped exactly once

	second := save(first)
	assert.Equal(t, string(second), string(first)) // what the UI got back re-saves unchanged
}
