package service

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IceWhaleTech/CasaOS-AppManagement/codegen"
	"github.com/IceWhaleTech/CasaOS-AppManagement/common"
	"github.com/IceWhaleTech/CasaOS-AppManagement/pkg/config"
	"github.com/IceWhaleTech/CasaOS-Common/utils/file"
	"github.com/IceWhaleTech/CasaOS-Common/utils/logger"
	"github.com/docker/compose/v2/pkg/api"
	"go.uber.org/zap"
)

const (
	defaultStorageMountPoint = "/DATA"
	procMountsPath           = "/proc/mounts"
	stoppedAppsDirName       = "stopped-apps"
	storageWaitWindow        = 30 * time.Minute
)

// When AppManagement starts, containers that depend on the default storage
// (the /DATA mergerfs mount) may already have failed to start, because
// docker starts them before the storage is mounted. Until /DATA shows up
// (or the wait window expires), a periodic sweep starts those abandoned
// apps again. Apps the user explicitly stopped are never restarted.

// isMergerFSLiveMount reports whether mountPoint is currently mounted as
// mergerfs, based on the contents of a /proc/mounts-like file.
func isMergerFSLiveMount(mountsFilePath, mountPoint string) bool {
	mountsFile, err := os.Open(mountsFilePath)
	if err != nil {
		return false
	}
	defer mountsFile.Close()

	scanner := bufio.NewScanner(mountsFile)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 3 && fields[1] == mountPoint && fields[2] == "fuse.mergerfs" {
			return true
		}
	}
	return false
}

func stoppedAppsDir() string {
	return filepath.Join(config.CommonInfo.RuntimePath, stoppedAppsDirName)
}

func stoppedAppMarkerPath(appName string) string {
	return filepath.Join(stoppedAppsDir(), appName)
}

// markAppStopped records that the user explicitly stopped the app, so that
// automatic recovery does not start it again.
func markAppStopped(appName string) {
	if err := file.IsNotExistMkDir(stoppedAppsDir()); err != nil {
		logger.Error("failed to create stopped apps dir", zap.Error(err))
		return
	}
	if err := file.CreateFileAndWriteContent(stoppedAppMarkerPath(appName), ""); err != nil {
		logger.Error("failed to record app stopped by user", zap.Error(err), zap.String("name", appName))
	}
}

func clearAppStopped(appName string) {
	if err := os.Remove(stoppedAppMarkerPath(appName)); err != nil && !os.IsNotExist(err) {
		logger.Error("failed to clear stopped app marker", zap.Error(err), zap.String("name", appName))
	}
}

func isAppStoppedByUser(appName string) bool {
	_, err := os.Stat(stoppedAppMarkerPath(appName))
	return err == nil
}

func (s *ComposeService) containerStates(ctx context.Context, projectName string) ([]string, error) {
	apiService, dockerClient, err := apiService()
	if err != nil {
		return nil, err
	}
	defer dockerClient.Close()

	summaries, err := apiService.Ps(ctx, projectName, api.PsOptions{All: true})
	if err != nil {
		return nil, err
	}

	states := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		states = append(states, summary.State)
	}

	return states, nil
}

// RecoverAppsWaitingForStorage starts compose apps that were abandoned
// because the default storage was not ready when they first tried to start.
// It is meant to be called periodically after service startup; it stops
// acting once the recovery pass has run or the wait window has expired.
func (s *ComposeService) RecoverAppsWaitingForStorage(ctx context.Context) {
	if s._recoveryDone.Load() {
		return
	}

	if time.Since(s._recoveryStartedAt) > storageWaitWindow {
		s._recoveryDone.Store(true)
		return
	}

	if !isMergerFSLiveMount(procMountsPath, defaultStorageMountPoint) {
		return
	}

	s._recoveryDone.Store(true)

	apps, err := s.List(ctx)
	if err != nil {
		logger.Error("failed to list compose apps for recovery", zap.Error(err))
		return
	}

	for name, app := range apps {
		if isAppStoppedByUser(name) {
			continue
		}

		states, err := s.containerStates(ctx, app.Name)
		if err != nil {
			logger.Error("failed to get container states", zap.Error(err), zap.String("name", name))
			continue
		}

		// only recover apps whose containers were started at least once and
		// are all exited now; "created" containers were never started, e.g.
		// because the installation failed
		if len(states) == 0 {
			continue
		}
		abandoned := true
		for _, state := range states {
			if state != "exited" {
				abandoned = false
				break
			}
		}
		if !abandoned {
			continue
		}

		logger.Info("starting app that was blocked by storage", zap.String("name", name))
		// the service context has no event properties, so add an empty set;
		// SetStatus assigns into the properties map
		if err := app.SetStatus(common.WithProperties(ctx, map[string]string{}), codegen.RequestComposeAppStatusStart); err != nil {
			logger.Error("failed to start app during recovery", zap.Error(err), zap.String("name", name))
		}
	}
}
