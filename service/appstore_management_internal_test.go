package service

import (
	"testing"

	"github.com/IceWhaleTech/CasaOS-AppManagement/codegen"
	"github.com/IceWhaleTech/CasaOS-AppManagement/common"
	"github.com/IceWhaleTech/CasaOS-AppManagement/pkg/config"
	pkg_utils "github.com/IceWhaleTech/CasaOS-AppManagement/pkg/utils"
	"github.com/IceWhaleTech/CasaOS-Common/utils"
	"github.com/IceWhaleTech/CasaOS-Common/utils/logger"
	"gotest.tools/v3/assert"
)

// fixtureAppStore serves a fixed catalogue - enough for CategoryMap(), which falls back to the
// default appstore when no appstore is registered.
type fixtureAppStore struct {
	catalog map[string]*ComposeApp
}

func (s fixtureAppStore) Catalog() (map[string]*ComposeApp, error) { return s.catalog, nil }

func (s fixtureAppStore) CategoryMap() (map[string]codegen.CategoryInfo, error) {
	return map[string]codegen.CategoryInfo{"test": {Name: utils.Ptr("test"), Count: utils.Ptr(42)}}, nil
}

func (s fixtureAppStore) ComposeApp(id string) (*ComposeApp, error) { return nil, nil }
func (s fixtureAppStore) Recommend() ([]string, error)              { return nil, nil }
func (s fixtureAppStore) UpdateCatalog() error                      { return nil }
func (s fixtureAppStore) WorkDir() (string, error)                  { return "", nil }

// Category counts must match what the list endpoint returns, which is architecture-filtered.
func TestCategoryMapCountsOnlySupportedApps(t *testing.T) {
	logger.LogInitConsoleOnly()

	appStoreList := config.ServerInfo.AppStoreList
	config.ServerInfo.AppStoreList = nil
	t.Cleanup(func() { config.ServerInfo.AppStoreList = appStoreList })

	app := func(architectures ...string) *ComposeApp {
		storeInfo := map[string]interface{}{"category": "test"}
		if architectures != nil {
			storeInfo["architectures"] = architectures
		}
		return &ComposeApp{Extensions: map[string]interface{}{common.ComposeExtensionNameXCasaOS: storeInfo}}
	}

	a := &AppStoreManagement{defaultAppStore: fixtureAppStore{catalog: map[string]*ComposeApp{
		"universal": app(),                       // no architectures - runs anywhere
		"host":      app(pkg_utils.GetCPUArch()), // runs here
		"alien":     app("nonexistent-arch"),     // runs nowhere - must not be counted
	}}}

	categoryMap, err := a.CategoryMap()
	assert.NilError(t, err)
	assert.Equal(t, *categoryMap["test"].Count, 2)
}
