package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	pb "github.com/infracost/proto/gen/go/infracost/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// checkByID returns the check with the given ID and whether it was present.
func checkByID(res ValidationResult, id string) (CheckResult, bool) {
	for _, c := range res.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return CheckResult{}, false
}

func statusOf(t *testing.T, res ValidationResult, id string) CheckStatus {
	t.Helper()
	c, ok := checkByID(res, id)
	require.Truef(t, ok, "expected a %q check in checklist", id)
	return c.Status
}

// stubValidator builds a binaryValidator whose stat/probe are driven by
// per-path maps, so tests never spawn real plugin subprocesses.
func stubValidator(stat map[string]error, probe map[string]probeResult) *binaryValidator {
	return &binaryValidator{
		stat: func(path string) error {
			if err, ok := stat[path]; ok {
				return err
			}
			return nil
		},
		probe: func(path string) probeResult {
			return probe[path]
		},
	}
}

func parserProbe(name, version string) probeResult {
	return probeResult{
		info:         &pb.GetPluginInfoResponse{Type: pb.PluginType_PARSER, Name: name, Version: version},
		parserConfig: &pb.GetParserConfigResponse{},
	}
}

func providerProbe(name, version string) probeResult {
	return probeResult{
		info: &pb.GetPluginInfoResponse{Type: pb.PluginType_PROVIDER, Name: name, Version: version},
	}
}

func TestValidateBinaryHealthyParserAllPass(t *testing.T) {
	path := "/plugins/infracost-parser-terraform"
	v := stubValidator(nil, map[string]probeResult{path: parserProbe("infracost/terraform", "1.2.3")})

	res := v.validateBinary(path, "")

	assert.True(t, res.OK(), "healthy parser should pass")
	assert.Equal(t, CheckPass, statusOf(t, res, CheckBinaryFile))
	assert.Equal(t, CheckPass, statusOf(t, res, CheckHandshake))
	assert.Equal(t, CheckPass, statusOf(t, res, CheckMetadata))
	assert.Equal(t, CheckPass, statusOf(t, res, CheckParserSurf))

	meta, _ := checkByID(res, CheckMetadata)
	assert.Contains(t, meta.Detail, "name=infracost/terraform")
	assert.Contains(t, meta.Detail, "version=1.2.3")
}

func TestValidateBinaryHealthyProviderAllPass(t *testing.T) {
	path := "/plugins/infracost-provider-aws"
	v := stubValidator(nil, map[string]probeResult{path: providerProbe("infracost/aws", "2.0.0")})

	res := v.validateBinary(path, "")

	assert.True(t, res.OK())
	assert.Equal(t, CheckPass, statusOf(t, res, CheckProviderSur))
	// Parser-surface check must not appear for a provider.
	_, hasParser := checkByID(res, CheckParserSurf)
	assert.False(t, hasParser)
}

func TestValidateBinaryNonExecutableFailsCheckOne(t *testing.T) {
	path := "/plugins/infracost-parser-terraform"
	v := stubValidator(
		map[string]error{path: errors.New("plugin binary not executable: " + path + " (try: chmod +x " + path + ")")},
		nil,
	)

	res := v.validateBinary(path, "")

	assert.False(t, res.OK())
	file, _ := checkByID(res, CheckBinaryFile)
	assert.Equal(t, CheckFail, file.Status)
	assert.Contains(t, file.Detail, "chmod +x")
	// Later checks are skipped, not run.
	assert.Equal(t, CheckSkip, statusOf(t, res, CheckHandshake))
	assert.Equal(t, CheckSkip, statusOf(t, res, CheckMetadata))
}

func TestValidateBinarySidecarPathFailsCheckOne(t *testing.T) {
	path := "/plugins/infracost-parser-terraform.sha256"
	v := stubValidator(nil, nil)

	res := v.validateBinary(path, "")

	assert.False(t, res.OK())
	file, _ := checkByID(res, CheckBinaryFile)
	assert.Equal(t, CheckFail, file.Status)
	assert.Contains(t, file.Detail, "not a plugin binary")
}

func TestValidateBinaryHandshakeFailure(t *testing.T) {
	path := "/plugins/not-a-plugin"
	v := stubValidator(nil, map[string]probeResult{
		path: {handshakeErr: errors.New("plugin handshake failed: exec format error")},
	})

	res := v.validateBinary(path, "")

	assert.False(t, res.OK())
	assert.Equal(t, CheckPass, statusOf(t, res, CheckBinaryFile))
	hs, _ := checkByID(res, CheckHandshake)
	assert.Equal(t, CheckFail, hs.Status)
	assert.Contains(t, hs.Detail, "exec format error")
	assert.Equal(t, CheckSkip, statusOf(t, res, CheckMetadata))
}

func TestValidateBinaryEmptyNameFailsMetadata(t *testing.T) {
	path := "/plugins/empty-name"
	v := stubValidator(nil, map[string]probeResult{
		path: {info: &pb.GetPluginInfoResponse{Type: pb.PluginType_PARSER, Name: "", Version: "1.0.0"}},
	})

	res := v.validateBinary(path, "")

	assert.False(t, res.OK())
	meta, _ := checkByID(res, CheckMetadata)
	assert.Equal(t, CheckFail, meta.Status)
	assert.Contains(t, meta.Detail, "name")
	// Surface check skipped after metadata failure.
	assert.Equal(t, CheckSkip, statusOf(t, res, CheckParserSurf))
}

func TestValidateBinaryUnknownTypeFailsMetadata(t *testing.T) {
	path := "/plugins/unknown-type"
	v := stubValidator(nil, map[string]probeResult{
		path: {info: &pb.GetPluginInfoResponse{Type: pb.PluginType(99), Name: "x/y", Version: "1.0.0"}},
	})

	res := v.validateBinary(path, "")

	assert.False(t, res.OK())
	meta, _ := checkByID(res, CheckMetadata)
	assert.Equal(t, CheckFail, meta.Status)
	assert.Contains(t, meta.Detail, "unknown plugin type")
}

func TestValidateBinaryParserConfigRPCFailure(t *testing.T) {
	path := "/plugins/broken-parser"
	v := stubValidator(nil, map[string]probeResult{
		path: {
			info:      &pb.GetPluginInfoResponse{Type: pb.PluginType_PARSER, Name: "x/y", Version: "1.0.0"},
			parserErr: errors.New("failed to get parser config: rpc error: boom"),
		},
	})

	res := v.validateBinary(path, "")

	assert.False(t, res.OK())
	assert.Equal(t, CheckPass, statusOf(t, res, CheckMetadata))
	surf, _ := checkByID(res, CheckParserSurf)
	assert.Equal(t, CheckFail, surf.Status)
	assert.Contains(t, surf.Detail, "boom")
}

func TestValidateBinaryChecklistShapeStableOnEarlyFailure(t *testing.T) {
	// Even when validation stops at check 1, checks 2–4 appear as skips so
	// --json consumers see a consistent shape.
	path := "/plugins/infracost-parser-terraform.version"
	v := stubValidator(nil, nil)

	res := v.validateBinary(path, "")

	for _, id := range []string{CheckBinaryFile, CheckHandshake, CheckMetadata, CheckParserSurf} {
		_, ok := checkByID(res, id)
		assert.Truef(t, ok, "expected %q in checklist", id)
	}
}

func TestValidateDirValidatesEachBinaryAndFailsIfAnyFail(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "infracost-parser-terraform")
	bad := filepath.Join(dir, "infracost-provider-aws")
	require.NoError(t, os.WriteFile(good, []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(bad, []byte("x"), 0o600))
	// A sidecar that must be skipped entirely.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "infracost-parser-terraform.version"), []byte("1.0.0"), 0o600))

	v := stubValidator(nil, map[string]probeResult{
		good: parserProbe("infracost/terraform", "1.0.0"),
		bad:  {handshakeErr: errors.New("plugin handshake failed")},
	})

	results, err := v.validateDir(dir)
	require.NoError(t, err)
	require.Len(t, results, 2, "sidecar should be skipped, two binaries validated")

	byPath := map[string]ValidationResult{}
	for _, r := range results {
		byPath[r.Path] = r
	}
	assert.True(t, byPath[good].OK())
	assert.False(t, byPath[bad].OK())
	assert.False(t, allDirOK(results))
}

func TestValidateDirEmptyDir(t *testing.T) {
	dir := t.TempDir()
	v := stubValidator(nil, nil)

	results, err := v.validateDir(dir)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestValidateDirMissingDir(t *testing.T) {
	v := stubValidator(nil, nil)
	results, err := v.validateDir(filepath.Join(t.TempDir(), "missing"))
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestValidateDirDetectsNameCollision(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "infracost-parser-terraform")
	b := filepath.Join(dir, "custom-terraform")
	require.NoError(t, os.WriteFile(a, []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(b, []byte("x"), 0o600))

	// Both report the same (name, type) — discovery would hard-fail on this.
	v := stubValidator(nil, map[string]probeResult{
		a: parserProbe("infracost/terraform", "1.0.0"),
		b: parserProbe("infracost/terraform", "9.9.9"),
	})

	results, err := v.validateDir(dir)
	require.NoError(t, err)
	require.Len(t, results, 2)

	for _, r := range results {
		c, ok := checkByID(r, CheckCollision)
		require.True(t, ok)
		assert.Equal(t, CheckWarn, c.Status, "collision should be a warning")
		// A warning does not fail validation.
		assert.True(t, r.OK())
	}
}

func TestValidateDirNoCollisionAcrossTypes(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "infracost-parser-kubernetes")
	b := filepath.Join(dir, "infracost-provider-kubernetes")
	require.NoError(t, os.WriteFile(a, []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(b, []byte("x"), 0o600))

	// Same name, different type — legitimately distinct, no collision.
	v := stubValidator(nil, map[string]probeResult{
		a: parserProbe("infracost/kubernetes", "1.0.0"),
		b: providerProbe("infracost/kubernetes", "1.0.0"),
	})

	results, err := v.validateDir(dir)
	require.NoError(t, err)
	for _, r := range results {
		assert.Equal(t, CheckPass, statusOf(t, r, CheckCollision))
	}
}

func TestValidateBinaryCollisionAgainstDir(t *testing.T) {
	dir := t.TempDir()
	sibling := filepath.Join(dir, "infracost-parser-terraform")
	require.NoError(t, os.WriteFile(sibling, []byte("x"), 0o600))

	target := "/elsewhere/custom-terraform"
	v := stubValidator(nil, map[string]probeResult{
		target:  parserProbe("infracost/terraform", "1.0.0"),
		sibling: parserProbe("infracost/terraform", "2.0.0"),
	})

	res := v.validateBinary(target, dir)
	c, ok := checkByID(res, CheckCollision)
	require.True(t, ok)
	assert.Equal(t, CheckWarn, c.Status)
	assert.Contains(t, c.Detail, sibling)
}

// TestValidateBinaryRealNonPluginHandshakeFails exercises the real dial path
// (which the stubbed tests bypass) against an executable that isn't a
// go-plugin. It must fail the handshake check, not hang: the whole thing is
// bounded by the go-plugin start timeout.
func TestValidateBinaryRealNonPluginHandshakeFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on a POSIX system executable")
	}
	src := "/bin/echo"
	data, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("no %s to copy: %v", src, err)
	}

	dir := t.TempDir()
	dst := filepath.Join(dir, "infracost-parser-fake")
	require.NoError(t, os.WriteFile(dst, data, 0o755)) //nolint:gosec // test needs an executable copy

	done := make(chan ValidationResult, 1)
	go func() { done <- ValidateBinary(dst, "") }()

	select {
	case res := <-done:
		assert.False(t, res.OK(), "a non-plugin executable must not validate")
		assert.Equal(t, CheckPass, statusOf(t, res, CheckBinaryFile))
		assert.Equal(t, CheckFail, statusOf(t, res, CheckHandshake), "handshake against a non-plugin must fail")
	case <-time.After(90 * time.Second):
		t.Fatal("validate hung on a non-plugin binary instead of reporting a bounded handshake failure")
	}
}

// allDirOK mirrors the command-side helper for asserting overall status.
func allDirOK(results []ValidationResult) bool {
	for _, r := range results {
		if !r.OK() {
			return false
		}
	}
	return true
}
