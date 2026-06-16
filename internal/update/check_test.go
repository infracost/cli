package update

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/infracost/cli/version"
)

func TestUpgradeCommand(t *testing.T) {
	cases := map[InstallMethod]string{
		InstallMethodBrew:       "$ brew upgrade infracost",
		InstallMethodChocolatey: "$ choco upgrade infracost",
		InstallMethodUnknown:    "$ infracost update",
	}
	for method, want := range cases {
		if got := method.UpgradeCommand(); got != want {
			t.Errorf("UpgradeCommand(%v) = %q, want %q", method, got, want)
		}
	}
}

func TestManagedExternally(t *testing.T) {
	if !InstallMethodBrew.ManagedExternally() {
		t.Error("brew should be managed externally")
	}
	if !InstallMethodChocolatey.ManagedExternally() {
		t.Error("chocolatey should be managed externally")
	}
	if InstallMethodUnknown.ManagedExternally() {
		t.Error("unknown should not be managed externally")
	}
}

func TestGetLatestBrewVersion(t *testing.T) {
	withHTTPGet(t, func(url string) ([]byte, error) {
		if url != "https://formulae.brew.sh/api/formula/infracost.json" {
			t.Fatalf("unexpected url: %s", url)
		}
		return []byte(`{"versions":{"stable":"2.1.3"}}`), nil
	})

	v, err := getLatestBrewVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != "v2.1.3" {
		t.Errorf("got %q, want v2.1.3", v)
	}
}

func TestGetLatestChocolateyVersion(t *testing.T) {
	withHTTPGet(t, func(_ string) ([]byte, error) {
		return []byte(`<?xml version="1.0" encoding="utf-8"?>
<feed>
  <entry>
    <properties>
      <Version>2.0.1</Version>
    </properties>
  </entry>
</feed>`), nil
	})

	v, err := getLatestChocolateyVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != "v2.0.1" {
		t.Errorf("got %q, want v2.0.1", v)
	}
}

func TestCheckForUpdate_NewerVersion(t *testing.T) {
	withVersion(t, "1.0.0")
	withInstallMethod(t, InstallMethodBrew)
	withTempCachePath(t)
	withHTTPGet(t, func(_ string) ([]byte, error) {
		return []byte(`{"versions":{"stable":"2.0.0"}}`), nil
	})

	info, err := CheckForUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info == nil {
		t.Fatal("expected an update notice, got nil")
	}
	if info.LatestVersion != "v2.0.0" {
		t.Errorf("got version %q, want v2.0.0", info.LatestVersion)
	}
	if info.Cmd != "$ brew upgrade infracost" {
		t.Errorf("got cmd %q, want brew upgrade", info.Cmd)
	}
}

func TestCheckForUpdate_AlreadyLatest_Brew(t *testing.T) {
	withVersion(t, "2.0.0")
	withInstallMethod(t, InstallMethodBrew)
	withTempCachePath(t)
	withHTTPGet(t, func(_ string) ([]byte, error) {
		return []byte(`{"versions":{"stable":"2.0.0"}}`), nil
	})

	info, err := CheckForUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Errorf("expected no update notice, got %+v", info)
	}
}

func TestCheckForUpdate_UsesCacheWithinTTL(t *testing.T) {
	withVersion(t, "1.0.0")
	withInstallMethod(t, InstallMethodBrew)
	withTempCachePath(t)

	// Seed cache so the network call should NOT happen.
	if err := saveCheckCache(&checkCache{
		LatestVersion: "v1.5.0",
		CheckedAt:     time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	calls := 0
	withHTTPGet(t, func(_ string) ([]byte, error) {
		calls++
		return []byte(`{"versions":{"stable":"9.9.9"}}`), nil
	})

	info, err := CheckForUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("expected cache hit, got %d HTTP calls", calls)
	}
	if info == nil || info.LatestVersion != "v1.5.0" {
		t.Errorf("expected cached v1.5.0, got %+v", info)
	}
}

func TestCheckForUpdate_RefetchesAfterTTL(t *testing.T) {
	withVersion(t, "1.0.0")
	withInstallMethod(t, InstallMethodBrew)
	withTempCachePath(t)

	if err := saveCheckCache(&checkCache{
		LatestVersion: "v1.5.0",
		CheckedAt:     time.Now().Add(-2 * checkCacheTTL),
	}); err != nil {
		t.Fatal(err)
	}

	withHTTPGet(t, func(_ string) ([]byte, error) {
		return []byte(`{"versions":{"stable":"3.0.0"}}`), nil
	})

	info, err := CheckForUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || info.LatestVersion != "v3.0.0" {
		t.Errorf("expected refetched v3.0.0, got %+v", info)
	}
}

func TestSkipUpdateCheck_DevBuild(t *testing.T) {
	withVersion(t, "dev")
	withTempCachePath(t)

	info, err := CheckForUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Errorf("dev build should skip the update check, got %+v", info)
	}
}

func TestSkipUpdateCheck_EnvOptOut(t *testing.T) {
	withVersion(t, "1.0.0")
	t.Setenv("INFRACOST_SKIP_UPDATE_CHECK", "1")

	info, err := CheckForUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Errorf("env opt-out should skip the update check, got %+v", info)
	}
}

func TestCheckCache_RoundTrip(t *testing.T) {
	withTempCachePath(t)

	now := time.Now().Truncate(time.Second)
	if err := saveCheckCache(&checkCache{LatestVersion: "v1.2.3", CheckedAt: now}); err != nil {
		t.Fatal(err)
	}

	got, err := loadCheckCache()
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestVersion != "v1.2.3" {
		t.Errorf("got version %q, want v1.2.3", got.LatestVersion)
	}
	if !got.CheckedAt.Equal(now) {
		t.Errorf("got time %v, want %v", got.CheckedAt, now)
	}
}

// withHTTPGet swaps in a stub httpGet for the duration of the test.
func withHTTPGet(t *testing.T, stub func(url string) ([]byte, error)) {
	t.Helper()
	orig := httpGet
	httpGet = stub
	t.Cleanup(func() { httpGet = orig })
}

// withInstallMethod pins DetectInstallMethod for the duration of the test.
func withInstallMethod(t *testing.T, m InstallMethod) {
	t.Helper()
	orig := DetectInstallMethod
	DetectInstallMethod = func() InstallMethod { return m }
	t.Cleanup(func() { DetectInstallMethod = orig })
}

// withVersion overrides version.Version for the duration of the test. The
// test runner detection in skipUpdateCheck would otherwise opt out of the
// network paths we're trying to exercise, so we also clear INFRACOST_SKIP_*.
func withVersion(t *testing.T, v string) {
	t.Helper()
	orig := version.Version
	version.Version = v
	t.Setenv("INFRACOST_SKIP_UPDATE_CHECK", "")
	// Bypass the .test binary detection so the goroutine actually runs.
	origIsTest := isTestBinaryFn
	isTestBinaryFn = func() bool { return false }
	t.Cleanup(func() {
		version.Version = orig
		isTestBinaryFn = origIsTest
	})
}

// withTempCachePath redirects the on-disk cache to a temp dir.
func withTempCachePath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	orig := checkCachePath
	checkCachePath = func() string { return filepath.Join(dir, "update-check.json") }
	t.Cleanup(func() { checkCachePath = orig })
}

// jsonRoundTrip is a sanity check that the cache file is human-readable.
// It is harmless if it fails on a future format change — adjust both the
// writer and this test together.
func TestCheckCache_JSONShape(t *testing.T) {
	withTempCachePath(t)

	now := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	if err := saveCheckCache(&checkCache{LatestVersion: "v1.0.0", CheckedAt: now}); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(checkCachePath())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("cache file is not valid JSON: %v", err)
	}
	if m["latestVersion"] != "v1.0.0" {
		t.Errorf("got latestVersion=%v, want v1.0.0", m["latestVersion"])
	}
}
