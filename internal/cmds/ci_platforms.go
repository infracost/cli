package cmds

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/ui"
)

// ciPlatform is a CI platform `ci setup --pipeline` can detect, and configure
// when a writer is registered for it.
type ciPlatform struct {
	id          string   // stable, what --ci-platform takes
	name        string   // display only, may be reworded freely
	configPaths []string // repo-relative candidates (globs), most canonical first
	docsPath    string   // recipe slug under ciDocsBase
	writer      ciWriter // nil until that platform's writer lands
	// ownsFiles marks a writer that replaces whole config files rather than
	// splicing into one the user wrote, so an existing file is a prompt.
	ownsFiles bool
}

// ciWriter writes the Infracost job into one platform's config files.
type ciWriter interface {
	// ConfigPaths returns the repo-relative files the job belongs in, whether
	// they exist yet or not. It errors when the platform's layout is not
	// something we can safely write to.
	ConfigPaths(repoRoot string) ([]string, error)
	Write(repoRoot string, opts ciJobOpts) ([]ciWriteResult, error)
	// Steps are the manual token and secret instructions this platform still
	// needs from the user, printed after the write. It takes repoRoot because
	// what is left to do can depend on what the user's config already has.
	Steps(repoRoot string, opts ciJobOpts) []string
}

// ciSecretSetter is implemented only by platforms where the CLI can set the API
// key itself. GitHub has `gh secret set`; nobody else is assumed to have glab
// or az installed.
type ciSecretSetter interface {
	// CanSetSecret reports whether the tool it shells out to is installed.
	CanSetSecret() bool
	SetSecret(ctx context.Context, opts ciJobOpts) error
}

type ciJobOpts struct {
	image         string // ghcr.io/infracost/ci:<tag>
	repo          repoInfo
	defaultBranch string
	apiKeySecret  string // name only, never the value
}

type ciWriteResult struct {
	path      string
	created   bool   // false = the file already existed
	unchanged bool   // the file already matched what we would write
	block     string // set when the block could not be placed; nothing was written
	reason    string // why the block could not be placed
}

const (
	// ciImage is the infracost/ci container written into generated CI jobs.
	// The floating minor tag, so a patch release reaches users without a CLI
	// release or a PR in their repo, at the cost of a non-reproducible job.
	ciImage = "ghcr.io/infracost/ci:0.1"

	// ciAPIKeySecret names both the secret the job reads and the environment
	// variable the CLI takes its value from.
	ciAPIKeySecret = "INFRACOST_API_KEY" //nolint:gosec // G101: a secret name, not a credential

	ciDocsBase = "https://www.infracost.io/docs/integrations/"
)

// VCS hosts, as identified from the git remote.
const (
	hostGitHub    = "github"
	hostGitLab    = "gitlab"
	hostAzure     = "azure"
	hostBitbucket = "bitbucket"
)

var supportedCIPlatforms = []ciPlatform{
	{
		id:          hostGitHub,
		name:        "GitHub Actions",
		configPaths: []string{".github/workflows/*.y*ml"},
		docsPath:    "github_actions",
		writer:      githubWriter{},
		ownsFiles:   true,
	},
	{
		id:          hostGitLab,
		name:        "GitLab CI",
		configPaths: []string{gitlabConfigPath},
		docsPath:    "gitlab_ci",
		writer:      gitlabWriter{},
	},
	{
		id:          hostAzure,
		name:        "Azure Pipelines",
		configPaths: azureConfigPaths,
		docsPath:    "azure_pipelines",
		writer:      azureWriter{},
	},
	{
		id:          hostBitbucket,
		name:        "Bitbucket Pipelines",
		configPaths: []string{bitbucketConfigPath},
		docsPath:    "bitbucket_pipelines",
		writer:      bitbucketWriter{},
	},
	{
		// A Jenkinsfile is hand-written Groovy owned by whoever runs the
		// platform. This entry exists so the platform is named rather than
		// falling to generic, and never gets a writer.
		id:          "jenkins",
		name:        "Jenkins",
		configPaths: []string{"Jenkinsfile"},
		docsPath:    "jenkins",
	},
	{
		id:       "generic",
		name:     "Generic CI/CD",
		docsPath: "cicd",
	},
}

func (p ciPlatform) docsURL() string {
	return ciDocsBase + p.docsPath + "/"
}

// resolveCIPlatform picks the platform to configure: the explicit --ci-platform
// id, the one config file found in the repo, the user's pick when several
// matched, or the VCS host as a last suggestion before generic.
func resolveCIPlatform(repoRoot string, repo repoInfo, id string) (ciPlatform, error) {
	if id != "" {
		p, ok := ciPlatformByID(id)
		if !ok {
			return ciPlatform{}, fmt.Errorf("unknown CI platform %q — expected one of: %s", id, strings.Join(ciPlatformIDs(), ", "))
		}
		return p, nil
	}

	matched := detectCIPlatforms(repoRoot)
	switch len(matched) {
	case 0:
		if p, ok := ciPlatformForHost(repo); ok {
			return p, nil
		}
		p, _ := ciPlatformByID("generic")
		return p, nil
	case 1:
		return matched[0], nil
	default:
		return promptCIPlatform(matched)
	}
}

// detectCIPlatforms returns the registered platforms with a config file present
// under repoRoot, in registry order. The file is the signal that matters: it is
// the one the job has to go into.
func detectCIPlatforms(repoRoot string) []ciPlatform {
	var matched []ciPlatform
	for _, p := range supportedCIPlatforms {
		for _, pattern := range p.configPaths {
			hits, err := filepath.Glob(filepath.Join(repoRoot, filepath.FromSlash(pattern)))
			if err == nil && len(hits) > 0 {
				matched = append(matched, p)
				break
			}
		}
	}
	return matched
}

// ciPlatformForHost suggests the platform to offer when no config file matched.
// It is a suggestion, so a wrong guess costs a prompt rather than a bad write.
func ciPlatformForHost(repo repoInfo) (ciPlatform, bool) {
	kind := vcsHostKind(repo.host)
	if kind == "" {
		return ciPlatform{}, false
	}
	return ciPlatformByID(kind)
}

// vcsHostKind identifies the VCS from the remote host. Self-hosted installs
// match on their leading label, so github.acme.com is not an unknown host.
func vcsHostKind(host string) string {
	h := normalizedHost(host)
	switch {
	case matchesVCSHost(h, "github", "github.com"):
		return hostGitHub
	case matchesVCSHost(h, "gitlab", "gitlab.com"):
		return hostGitLab
	case matchesVCSHost(h, "bitbucket", "bitbucket.org"):
		return hostBitbucket
	case h == "dev.azure.com" || strings.HasSuffix(h, ".dev.azure.com") || strings.HasSuffix(h, ".visualstudio.com"):
		return hostAzure
	}
	return ""
}

// matchesVCSHost matches both a self-hosted install on its leading label
// (github.acme.com) and a subdomain of the SaaS host, which is how the
// port-443 SSH endpoints ssh.github.com and altssh.bitbucket.org arrive.
func matchesVCSHost(h, label, saas string) bool {
	return strings.HasPrefix(h, label+".") || h == saas || strings.HasSuffix(h, "."+saas)
}

func normalizedHost(host string) string {
	h := strings.ToLower(host)
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return h
}

// hasAppIntegration reports whether the dashboard app integration can connect
// this host. Pipeline mode works everywhere and the app integration works on a
// list, so anything unrecognized — self-hosted included — falls to pipeline
// mode rather than a dashboard page that will never accept the repository.
func hasAppIntegration(host string) bool {
	h := normalizedHost(host)
	switch h {
	// The alternate SSH endpoints are the same SaaS repositories on port 443.
	case "github.com", "ssh.github.com", "gitlab.com", "altssh.gitlab.com",
		"dev.azure.com", "ssh.dev.azure.com", "vs-ssh.visualstudio.com":
		return true
	}
	return strings.HasSuffix(h, ".visualstudio.com")
}

func ciPlatformByID(id string) (ciPlatform, bool) {
	for _, p := range supportedCIPlatforms {
		if p.id == id {
			return p, true
		}
	}
	return ciPlatform{}, false
}

func ciPlatformIDs() []string {
	ids := make([]string, 0, len(supportedCIPlatforms))
	for _, p := range supportedCIPlatforms {
		ids = append(ids, p.id)
	}
	return ids
}

func promptCIPlatform(matched []ciPlatform) (ciPlatform, error) {
	names := make([]string, 0, len(matched))
	ids := make([]string, 0, len(matched))
	for _, p := range matched {
		names = append(names, p.name)
		ids = append(ids, p.id)
	}

	if !ui.IsInteractive() {
		return ciPlatform{}, fmt.Errorf(
			"this repository has config for %s and there is no interactive terminal to choose between them — re-run with --ci-platform set to one of: %s",
			strings.Join(names, ", "), strings.Join(ids, ", "))
	}

	opts := make([]huh.Option[string], 0, len(matched))
	for _, p := range matched {
		opts = append(opts, huh.NewOption(p.name, p.id))
	}

	var selected string
	err := huh.NewSelect[string]().
		Title("This repository has config for more than one CI platform. Which should Infracost use?").
		Options(opts...).
		Value(&selected).
		WithTheme(ui.BrandTheme()).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return ciPlatform{}, fmt.Errorf("no CI platform selected")
		}
		return ciPlatform{}, err
	}

	p, _ := ciPlatformByID(selected)
	return p, nil
}
