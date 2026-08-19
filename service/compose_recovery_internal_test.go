package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IceWhaleTech/CasaOS-AppManagement/pkg/config"
	"github.com/IceWhaleTech/CasaOS-Common/utils/logger"
)

func setRuntimePathForTest(t *testing.T) {
	t.Helper()

	oldRuntimePath := config.CommonInfo.RuntimePath
	t.Cleanup(func() {
		config.CommonInfo.RuntimePath = oldRuntimePath
	})
	config.CommonInfo.RuntimePath = t.TempDir()
}

func TestIsMergerFSLiveMount(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", false},
		{"other mount only", "none / ext4 rw 0 0\n", false},
		{"data with wrong fstype", "/dev/sdb1 /DATA ext4 rw 0 0\n", false},
		{"data with mergerfs", "/mergerfs /DATA fuse.mergerfs rw 0 0\n", true},
		{"prefix collision", "/dev/sdb1 /DATAX ext4 rw 0 0\n", false},
		{"short line", "/dev/sdb1\n", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mounts")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := isMergerFSLiveMount(path, "/DATA"); got != tc.want {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestIsMergerFSLiveMountMissingFile(t *testing.T) {
	if isMergerFSLiveMount(filepath.Join(t.TempDir(), "nope"), "/DATA") {
		t.Fatal("expected a missing mounts file to be reported as not mounted")
	}
}

func TestStoppedAppMarkers(t *testing.T) {
	logger.LogInitConsoleOnly()
	setRuntimePathForTest(t)

	if isAppStoppedByUser("jellyfin") {
		t.Fatal("expected no stop marker initially")
	}

	markAppStopped("jellyfin")
	if !isAppStoppedByUser("jellyfin") {
		t.Fatal("expected a stop marker after markAppStopped")
	}

	clearAppStopped("jellyfin")
	if isAppStoppedByUser("jellyfin") {
		t.Fatal("expected no stop marker after clearAppStopped")
	}

	// clearing a missing marker is a no-op
	clearAppStopped("jellyfin")
	if isAppStoppedByUser("jellyfin") {
		t.Fatal("expected repeated clearAppStopped to be a no-op")
	}
}

func TestRecoverAppsWaitingForStorageWindowExpiry(t *testing.T) {
	logger.LogInitConsoleOnly()
	setRuntimePathForTest(t)

	svc := NewComposeService()
	svc._recoveryStartedAt = time.Now().Add(-storageWaitWindow - time.Minute)

	// must not panic; expiry marks the sweep as finished even when /DATA is
	// not a live mergerfs mount
	svc.RecoverAppsWaitingForStorage(context.Background())

	if !svc._recoveryDone.Load() {
		t.Fatal("expected the sweep to be marked done after the wait window expired")
	}
}

func TestRecoverAppsWaitingForStorageWaitsForStorage(t *testing.T) {
	if isMergerFSLiveMount(procMountsPath, defaultStorageMountPoint) {
		t.Skip("the test environment has a live /DATA mergerfs mount")
	}

	logger.LogInitConsoleOnly()
	setRuntimePathForTest(t)

	svc := NewComposeService()

	// /DATA is not mounted yet: the sweep must stay armed without acting
	svc.RecoverAppsWaitingForStorage(context.Background())

	if svc._recoveryDone.Load() {
		t.Fatal("expected the sweep to stay armed while storage is not ready")
	}
}
