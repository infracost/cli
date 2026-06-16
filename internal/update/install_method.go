package update

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// InstallMethod identifies how the running binary was installed. Brew and
// Chocolatey own their own state, so we delegate updates to them rather than
// fighting the package manager by self-replacing the binary.
type InstallMethod int

const (
	InstallMethodUnknown InstallMethod = iota
	InstallMethodBrew
	InstallMethodChocolatey
)

func (m InstallMethod) UpgradeCommand() string {
	switch m {
	case InstallMethodBrew:
		return "$ brew upgrade infracost"
	case InstallMethodChocolatey:
		return "$ choco upgrade infracost"
	default:
		return "$ infracost update"
	}
}

// ManagedExternally reports whether the package manager should drive the
// upgrade. The built-in `infracost update` refuses to run in that case.
func (m InstallMethod) ManagedExternally() bool {
	return m == InstallMethodBrew || m == InstallMethodChocolatey
}

// DetectInstallMethod is a var so tests can stub it. Detection is
// best-effort — any error is swallowed and treated as Unknown.
var DetectInstallMethod = func() InstallMethod {
	if isBrewInstall() {
		return InstallMethodBrew
	}
	if isChocolateyInstall() {
		return InstallMethodChocolatey
	}
	return InstallMethodUnknown
}

func isBrewInstall() bool {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return false
	}

	exe, err := resolvedExecutable()
	if err != nil {
		return false
	}

	cmd := exec.Command("brew", "--prefix", "infracost")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return false
	}

	brewPrefix, err := filepath.EvalSymlinks(strings.TrimSpace(stdout.String()))
	if err != nil {
		return false
	}

	return exe == filepath.Join(brewPrefix, "bin", "infracost")
}

func isChocolateyInstall() bool {
	if runtime.GOOS != "windows" {
		return false
	}

	exe, err := resolvedExecutable()
	if err != nil {
		return false
	}

	root := os.Getenv("ChocolateyInstall")
	if root == "" {
		root = `C:\ProgramData\chocolatey`
	}

	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}

	exe = strings.ToLower(exe)
	root = strings.ToLower(root)
	return strings.HasPrefix(exe, root+string(filepath.Separator))
}

func resolvedExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func getLatestBrewVersion() (string, error) {
	body, err := httpGet("https://formulae.brew.sh/api/formula/infracost.json")
	if err != nil {
		return "", err
	}

	var parsed struct {
		Versions struct {
			Stable string `json:"stable"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parsing brew formula: %w", err)
	}

	v := strings.TrimSpace(parsed.Versions.Stable)
	if v == "" {
		return "", fmt.Errorf("brew formula returned no stable version")
	}
	return ensureV(v), nil
}

// getLatestChocolateyVersion queries the OData feed for the entry flagged
// IsLatestVersion. The response is ATOM XML; we only need <Version> off the
// entry's properties block.
func getLatestChocolateyVersion() (string, error) {
	body, err := httpGet("https://community.chocolatey.org/api/v2/Packages()?$filter=Id%20eq%20%27infracost%27%20and%20IsLatestVersion")
	if err != nil {
		return "", err
	}

	var feed struct {
		Entries []struct {
			Properties struct {
				Version string `xml:"Version"`
			} `xml:"properties"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return "", fmt.Errorf("parsing chocolatey feed: %w", err)
	}

	for _, e := range feed.Entries {
		v := strings.TrimSpace(e.Properties.Version)
		if v != "" {
			return ensureV(v), nil
		}
	}
	return "", fmt.Errorf("chocolatey feed returned no version")
}

func ensureV(v string) string {
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

// httpGet is a package var so tests can swap in an httptest server.
var httpGet = func(url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}
