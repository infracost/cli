package parser

import (
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/infracost/cli/internal/protocache"
	repoconfig "github.com/infracost/config"
)

func createCacheKey(path, parserVersion string, cfg *repoconfig.Config, project *repoconfig.Project) protocache.Key {
	return protocache.Key(hash(
		path,
		parserVersion,
		globalConfigToString(cfg),
		projectConfigToString(project),
		latestModTime(path),
	))
}

// latestModTime recursively walks the given path and returns the most recent
// modification time (as a Unix nanosecond string) across all files. If the
// path cannot be walked, it returns an empty string so the cache key is still
// valid (just won't benefit from caching).
func latestModTime(root string) string {
	var latest int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if t := info.ModTime().UnixNano(); t > latest {
			latest = t
		}
		return nil
	})
	return strconv.FormatInt(latest, 10)
}

func hash(s ...string) string {
	h := fnv.New128a()
	for _, str := range s {
		h.Write([]byte(str))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func projectConfigToString(project *repoconfig.Project) string {
	return strings.Join(
		[]string{
			project.Name,
			project.Path,
			project.Terraform.Workspace,
			project.Terraform.Cloud.Host,
			project.Terraform.Cloud.Org,
			project.Terraform.Cloud.Token,
			project.Terraform.Cloud.Workspace,
			strings.Join(project.Terraform.VarFiles, "\x00"),
			mapAnyToString(project.Terraform.Vars),
			project.Terraform.Spacelift.APIKey.ID,
			project.Terraform.Spacelift.APIKey.Endpoint,
			project.Terraform.Spacelift.APIKey.Secret,
			string(project.Type),
			mapStringToString(project.Env),
			project.YorConfigPath,
			project.EnvName,
			strings.Join(project.DependencyPaths, "\x00"),
			strings.Join(project.ExcludePaths, "\x00"),
			project.AWS.StackID,
			project.AWS.StackName,
			project.AWS.Region,
			project.AWS.AccountID,
		},
		"\x00",
	)
}

func globalConfigToString(cfg *repoconfig.Config) string {
	return strings.Join(
		[]string{
			cfg.Terraform.Defaults.Workspace,
			cfg.Terraform.Defaults.Cloud.Host,
			cfg.Terraform.Defaults.Cloud.Org,
			cfg.Terraform.Defaults.Cloud.Token,
			cfg.Terraform.Defaults.Cloud.Workspace,
			cfg.Terraform.Defaults.Spacelift.APIKey.ID,
			cfg.Terraform.Defaults.Spacelift.APIKey.Endpoint,
			cfg.Terraform.Defaults.Spacelift.APIKey.Secret,
			func() string {
				var sb strings.Builder
				for _, mapping := range cfg.Terraform.SourceMap {
					sb.WriteString(mapping.Match)
					sb.WriteByte(0)
					sb.WriteString(mapping.Replace)
					sb.WriteByte(0)
				}
				return sb.String()
			}(),
			mapStringToString(cfg.CDK.Defaults.Context),
			mapStringToString(cfg.CDK.Defaults.Env),
		},
		"\x00",
	)
}

func mapStringToString(m map[string]string) string {
	var sb strings.Builder
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte(0)
		sb.WriteString(m[k])
		sb.WriteByte(0)
	}
	return sb.String()
}

func mapAnyToString(m map[string]any) string {
	var sb strings.Builder
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte(0)
		fmt.Fprintf(&sb, "%v", m[k])
		sb.WriteByte(0)
	}
	return sb.String()
}
