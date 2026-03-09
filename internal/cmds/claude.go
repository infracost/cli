package cmds

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/infracost/cli/internal/config"
	"github.com/spf13/cobra"
)

const (
	claudeSkillsMarketplace     = "infracost/claude-skills"
	claudeSkillsMarketplaceName = "infracost"
	claudeSkillsPlugin          = "infracost@infracost"
)

type ClaudeOptions struct {
	Scope string
}

func Claude(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claude",
		Short: "Manage Claude Code integration",
	}
	cmd.AddCommand(claudeEnable(cfg))
	cmd.AddCommand(claudeDisable(cfg))
	return cmd
}

func claudeEnable(cfg *config.Config) *cobra.Command {
	opts := new(ClaudeOptions)

	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Enable Infracost skills in Claude Code",
		Long:  "Registers the Infracost skills marketplace and installs the Infracost plugin into Claude Code. Restart Claude Code after running this command to activate the skills.",
		RunE: func(_ *cobra.Command, _ []string) error {
			claudePath := claudeBinary(cfg)
			if err := opts.preflight(claudePath); err != nil {
				return err
			}

			fmt.Println("Adding Infracost skills marketplace...")
			if err := runClaude(claudePath, "plugin", "marketplace", "add", claudeSkillsMarketplace); err != nil {
				return fmt.Errorf("adding marketplace: %w", err)
			}

			fmt.Println("Installing Infracost plugin...")
			if err := runClaude(claudePath, "plugin", "install", "--scope", opts.Scope, claudeSkillsPlugin); err != nil {
				return fmt.Errorf("installing plugin: %w", err)
			}

			fmt.Println("Infracost skills enabled. Restart Claude Code to activate.")
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Scope, "scope", "user", "Installation scope: user (global), project, or local")
	return cmd
}

func claudeDisable(cfg *config.Config) *cobra.Command {
	opts := new(ClaudeOptions)

	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Disable Infracost skills in Claude Code",
		Long:  "Uninstalls the Infracost plugin and removes the skills marketplace from Claude Code.",
		RunE: func(_ *cobra.Command, _ []string) error {
			claudePath := claudeBinary(cfg)
			if err := opts.preflight(claudePath); err != nil {
				return err
			}

			var errs []error

			fmt.Println("Uninstalling Infracost plugin...")
			if err := runClaude(claudePath, "plugin", "uninstall", "--scope", opts.Scope, claudeSkillsPlugin); err != nil {
				errs = append(errs, fmt.Errorf("uninstalling plugin: %w", err))
			}

			fmt.Println("Removing Infracost skills marketplace...")
			if err := runClaude(claudePath, "plugin", "marketplace", "remove", claudeSkillsMarketplaceName); err != nil {
				errs = append(errs, fmt.Errorf("removing marketplace: %w", err))
			}

			if len(errs) > 0 {
				return errors.Join(errs...)
			}

			fmt.Println("Infracost skills disabled.")
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Scope, "scope", "user", "Installation scope: user (global), project, or local")
	return cmd
}

var validScopes = map[string]struct{}{
	"user":    {},
	"project": {},
	"local":   {},
}

func claudeBinary(cfg *config.Config) string {
	if cfg.ClaudePath != "" {
		return cfg.ClaudePath
	}
	return "claude"
}

func (o *ClaudeOptions) preflight(claudePath string) error {
	if _, ok := validScopes[o.Scope]; !ok {
		return fmt.Errorf("invalid scope %q: must be one of user, project, or local", o.Scope)
	}
	if _, err := exec.LookPath(claudePath); err != nil {
		return fmt.Errorf("claude CLI not found on PATH. Install it from https://docs.anthropic.com/en/docs/claude-code")
	}
	return nil
}

func runClaude(claudePath string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.Command(claudePath, args...) //nolint:gosec // claudePath is user-configured via env/flag
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return err
	}
	return nil
}
