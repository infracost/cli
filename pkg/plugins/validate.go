package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	pb "github.com/infracost/proto/gen/go/infracost/plugin"
)

// CheckStatus is the outcome of a single validation check.
type CheckStatus string

const (
	// CheckPass means the check succeeded.
	CheckPass CheckStatus = "pass"
	// CheckFail means the check failed; the binary is not usable.
	CheckFail CheckStatus = "fail"
	// CheckSkip means the check did not run because a prerequisite failed.
	CheckSkip CheckStatus = "skip"
	// CheckWarn means the check surfaced a non-fatal problem.
	CheckWarn CheckStatus = "warn"
)

// Check identifiers, stable across releases so --json consumers (SDK CI
// pipelines) can key on them.
const (
	CheckBinaryFile  = "binary-file"
	CheckHandshake   = "handshake"
	CheckMetadata    = "metadata"
	CheckParserSurf  = "parser-surface"
	CheckProviderSur = "provider-surface"
	CheckCollision   = "name-collision"
)

// CheckResult is one line in a validation checklist.
type CheckResult struct {
	// Name is a short human-readable label for the check.
	Name string `json:"name"`
	// ID is the stable identifier (CheckBinaryFile, CheckHandshake, ...).
	ID string `json:"id"`
	// Status is pass/fail/skip/warn.
	Status CheckStatus `json:"status"`
	// Detail is the failure reason, warning text, or supplementary info
	// (e.g. reported name/version, config fields). Empty on a bare pass.
	Detail string `json:"detail,omitempty"`
}

// ValidationResult is the full checklist for one plugin binary.
type ValidationResult struct {
	// Path is the binary that was validated.
	Path string `json:"path"`
	// Checks are the checklist rows in check order.
	Checks []CheckResult `json:"checks"`
}

// OK reports whether the binary passed validation: no check failed. Warnings
// (e.g. a name collision) do not fail validation.
func (r ValidationResult) OK() bool {
	for _, c := range r.Checks {
		if c.Status == CheckFail {
			return false
		}
	}
	return true
}

// probeResult holds everything a single plugin subprocess lifetime yields for
// the checklist: the handshake outcome, GetPluginInfo, and (for parsers) the
// parser config. Each field's *Err is set only for the step that failed;
// later fields stay nil.
type probeResult struct {
	handshakeErr error
	info         *pb.GetPluginInfoResponse
	infoErr      error
	parserConfig *pb.GetParserConfigResponse
	parserErr    error
}

// binaryValidator carries the probing seams so tests can drive the checklist
// without spawning real plugin subprocesses. The zero value is not usable;
// use newBinaryValidator.
type binaryValidator struct {
	// stat performs check 1 (file exists, not a dir, executable).
	stat func(path string) error
	// probe performs the handshake + info + parser-config steps against a
	// single subprocess lifetime and always terminates it before returning.
	probe func(path string) probeResult
}

func newBinaryValidator() *binaryValidator {
	return &binaryValidator{stat: statPluginBinary, probe: probePluginBinary}
}

// probePluginBinary connects to the plugin at path and gathers the handshake,
// GetPluginInfo, and (for parsers) GetParserConfig results in one subprocess
// lifetime. The subprocess is always killed before returning. Each RPC is
// bounded by queryPluginInfoTimeout so a plugin that connects but then hangs
// reports a timeout rather than blocking forever.
func probePluginBinary(path string) probeResult {
	var res probeResult

	client, conn, _, err := dialPlugin(path)
	defer client.Kill()
	if err != nil {
		res.handshakeErr = err
		return res
	}

	infoCtx, cancel := context.WithTimeout(context.Background(), queryPluginInfoTimeout)
	defer cancel()
	info, err := pb.NewPluginServiceClient(conn).GetPluginInfo(infoCtx, &pb.GetPluginInfoRequest{})
	if err != nil {
		res.infoErr = fmt.Errorf("failed to get plugin info: %w", err)
		return res
	}
	if info == nil {
		res.infoErr = fmt.Errorf("plugin returned no info")
		return res
	}
	res.info = info

	if info.GetType() == pb.PluginType_PARSER {
		pcCtx, pcCancel := context.WithTimeout(context.Background(), queryPluginInfoTimeout)
		defer pcCancel()
		pc, err := pb.NewParserServiceClient(conn).GetParserConfig(pcCtx, &pb.GetParserConfigRequest{})
		if err != nil {
			res.parserErr = fmt.Errorf("failed to get parser config: %w", err)
			return res
		}
		res.parserConfig = pc
	}

	return res
}

// check runs checks 1–4 for one binary and returns the checklist plus the
// plugin's identity when it reported valid metadata (nil otherwise, so the
// caller can compute cross-binary collisions). Check 5 (collision) is appended
// by the orchestrator, which knows the other binaries in the directory.
func (v *binaryValidator) check(path string) (ValidationResult, *pluginIdentity) {
	res := ValidationResult{Path: path}
	add := func(id, name string, status CheckStatus, detail string) {
		res.Checks = append(res.Checks, CheckResult{ID: id, Name: name, Status: status, Detail: detail})
	}
	// skipRest marks every remaining check as skipped so the checklist shape
	// is stable for --json regardless of where validation stopped.
	skipRest := func(from int) {
		order := []struct{ id, name string }{
			{CheckHandshake, "go-plugin handshake"},
			{CheckMetadata, "plugin metadata"},
			{CheckParserSurf, "plugin RPC surface"},
		}
		for i := from; i < len(order); i++ {
			add(order[i].id, order[i].name, CheckSkip, "skipped: a prerequisite check failed")
		}
	}

	// Check 1: file exists, not a directory, executable.
	base := filepath.Base(path)
	if isPluginSidecar(base) {
		add(CheckBinaryFile, "binary file", CheckFail, "not a plugin binary (this is a plugin sidecar file)")
		skipRest(0)
		return res, nil
	}
	if err := v.stat(path); err != nil {
		add(CheckBinaryFile, "binary file", CheckFail, err.Error())
		skipRest(0)
		return res, nil
	}
	if detail := windowsExeNote(path); detail != "" {
		add(CheckBinaryFile, "binary file", CheckPass, detail)
	} else {
		add(CheckBinaryFile, "binary file", CheckPass, "")
	}

	// Checks 2–4 share one subprocess lifetime.
	pr := v.probe(path)

	// Check 2: handshake.
	if pr.handshakeErr != nil {
		add(CheckHandshake, "go-plugin handshake", CheckFail, pr.handshakeErr.Error())
		skipRest(1)
		return res, nil
	}
	add(CheckHandshake, "go-plugin handshake", CheckPass, "")

	// Check 3: metadata (non-empty name, non-empty version, known type).
	if pr.infoErr != nil {
		add(CheckMetadata, "plugin metadata", CheckFail, pr.infoErr.Error())
		skipRest(2)
		return res, nil
	}
	var problems []string
	name := pr.info.GetName()
	version := pr.info.GetVersion()
	typ := pr.info.GetType()
	if name == "" {
		problems = append(problems, "reported name is empty")
	}
	if version == "" {
		problems = append(problems, "reported version is empty")
	}
	if typ != pb.PluginType_PARSER && typ != pb.PluginType_PROVIDER {
		problems = append(problems, fmt.Sprintf("unknown plugin type %q (expected PARSER or PROVIDER)", typ))
	}
	if len(problems) > 0 {
		add(CheckMetadata, "plugin metadata", CheckFail, strings.Join(problems, "; "))
		skipRest(2)
		return res, nil
	}
	add(CheckMetadata, "plugin metadata", CheckPass, fmt.Sprintf("name=%s version=%s type=%s", name, version, pluginTypeString(typ)))

	// Check 4: type-specific RPC surface.
	switch typ {
	case pb.PluginType_PARSER:
		if pr.parserErr != nil {
			add(CheckParserSurf, "parser config", CheckFail, pr.parserErr.Error())
		} else {
			add(CheckParserSurf, "parser config", CheckPass, parserConfigSummary(pr.parserConfig))
		}
	case pb.PluginType_PROVIDER:
		add(CheckProviderSur, "provider surface", CheckPass, "provider service is dispensable")
	}

	return res, &pluginIdentity{name: name, typ: typ}
}

// windowsExeNote returns a note about the expected .exe suffix when validating
// a Windows binary that lacks it, or "" otherwise. On non-Windows it is always
// "". This is informational; it never fails the check.
func windowsExeNote(path string) string {
	if runtime.GOOS != "windows" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(path), ".exe") {
		return ""
	}
	return "note: plugin binaries are expected to have a .exe suffix on Windows"
}

func pluginTypeString(t pb.PluginType) string {
	switch t {
	case pb.PluginType_PARSER:
		return pluginTypeParser
	case pb.PluginType_PROVIDER:
		return pluginTypeProvider
	default:
		return t.String()
	}
}

// parserConfigSummary renders the parser config's key fields so SDK authors
// can eyeball what their plugin reports.
func parserConfigSummary(pc *pb.GetParserConfigResponse) string {
	if pc == nil {
		return ""
	}
	projectType := pc.GetConfigFileProjectType()
	if projectType == "" {
		projectType = "(none)"
	}
	return fmt.Sprintf("configFileProjectType=%s identificationPriority=%d", projectType, pc.GetIdentificationPriority())
}

// ValidateBinary validates the plugin binary at path against the CLI's plugin
// expectations, returning a checklist. When collisionDir is non-empty, a
// name-collision check warns if the binary's reported (name, type) matches
// another plugin already in that directory. Validation is purely local: no
// network, no registry.
func ValidateBinary(path, collisionDir string) ValidationResult {
	return newBinaryValidator().validateBinary(path, collisionDir)
}

func (v *binaryValidator) validateBinary(path, collisionDir string) ValidationResult {
	res, identity := v.check(path)
	appendCollisionCheck(v, &res, identity, collisionDir)
	return res
}

// appendCollisionCheck adds check 5 (name collision) to res when the binary
// reported valid metadata and collisionDir names other plugins. A collision is
// a warning, not a failure — but discovery would hard-fail on the pair, so the
// author should know.
func appendCollisionCheck(v *binaryValidator, res *ValidationResult, identity *pluginIdentity, collisionDir string) {
	if identity == nil || collisionDir == "" {
		return
	}
	other, ok := v.collidingSibling(collisionDir, res.Path, *identity)
	if ok {
		res.Checks = append(res.Checks, CheckResult{
			ID:     CheckCollision,
			Name:   "name collision",
			Status: CheckWarn,
			Detail: fmt.Sprintf("another %s plugin in %s reports the same name %q (%s); discovery would fail on this pair", pluginTypeString(identity.typ), collisionDir, identity.name, other),
		})
		return
	}
	res.Checks = append(res.Checks, CheckResult{ID: CheckCollision, Name: "name collision", Status: CheckPass, Detail: ""})
}

// collidingSibling probes every other plugin binary in dir looking for one
// that reports the same (name, type) as identity. Returns the colliding
// binary's path. selfPath is excluded.
func (v *binaryValidator) collidingSibling(dir, selfPath string, identity pluginIdentity) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	self := filepath.Clean(selfPath)
	for _, entry := range entries {
		if entry.IsDir() || isPluginSidecar(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if filepath.Clean(path) == self {
			continue
		}
		if v.stat(path) != nil {
			continue
		}
		pr := v.probe(path)
		if pr.info == nil {
			continue
		}
		if pr.info.GetName() == identity.name && pr.info.GetType() == identity.typ {
			return path, true
		}
	}
	return "", false
}

// ValidateDir validates every plugin binary in dir (skipping sidecars),
// returning one checklist per binary sorted by path. Cross-binary name
// collisions are reported on both members of a colliding pair. When the
// directory has no plugin binaries, an empty slice and a nil error are
// returned; a missing directory is treated the same way.
func ValidateDir(dir string) ([]ValidationResult, error) {
	return newBinaryValidator().validateDir(dir)
}

func (v *binaryValidator) validateDir(dir string) ([]ValidationResult, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var paths []string
	for _, entry := range entries {
		if entry.IsDir() || isPluginSidecar(entry.Name()) {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(paths)

	results := make([]ValidationResult, 0, len(paths))
	identities := make([]*pluginIdentity, 0, len(paths))
	for _, path := range paths {
		res, identity := v.check(path)
		results = append(results, res)
		identities = append(identities, identity)
	}

	// Collisions: two binaries in the directory reporting the same
	// (name, type). Computed from the already-collected identities so no
	// binary is probed twice.
	byIdentity := make(map[pluginIdentity][]int)
	for i, id := range identities {
		if id != nil {
			byIdentity[*id] = append(byIdentity[*id], i)
		}
	}
	for i := range results {
		id := identities[i]
		if id == nil {
			continue
		}
		peers := byIdentity[*id]
		var others []string
		for _, j := range peers {
			if j != i {
				others = append(others, results[j].Path)
			}
		}
		if len(others) > 0 {
			results[i].Checks = append(results[i].Checks, CheckResult{
				ID:     CheckCollision,
				Name:   "name collision",
				Status: CheckWarn,
				Detail: fmt.Sprintf("shares the reported name %q (%s) with: %s; discovery would fail on this pair", id.name, pluginTypeString(id.typ), strings.Join(others, ", ")),
			})
		} else {
			results[i].Checks = append(results[i].Checks, CheckResult{ID: CheckCollision, Name: "name collision", Status: CheckPass, Detail: ""})
		}
	}

	return results, nil
}
