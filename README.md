# cli

Infracost estimates cloud costs from infrastructure as code, helping you catch cost surprises before they hit your bill.
It currently supports Terraform, Terragrunt, and CloudFormation.

## Installation

The quickest way to install on macOS or Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/infracost/cli/main/scripts/install.sh | sh
```

To pin a specific version, set `INFRACOST_VERSION`:

```bash
curl -fsSL https://raw.githubusercontent.com/infracost/cli/main/scripts/install.sh | INFRACOST_VERSION=v2.0.0 sh
```

Or download the archive for your platform from the
[GitHub Releases page](https://github.com/infracost/cli/releases), extract the binary and place it on your `PATH`:

```bash
# macOS (Apple Silicon)
tar -xzf infracost-darwin-arm64.tar.gz
mkdir -p ~/.local/bin
mv infracost ~/.local/bin/infracost

# Linux (amd64)
tar -xzf infracost-linux-amd64.tar.gz
mkdir -p ~/.local/bin
mv infracost ~/.local/bin/infracost
```

The install script also prefers `~/.local/bin` when it is already on your `PATH`; otherwise it installs to `/usr/local/bin`.

On Windows, download the `.zip` archive and extract it to a directory on your `PATH`.

Once installed, verify it works:

```bash
infracost help
```

### Uninstalling

Remove the binary and the cached configuration/token data.

On macOS:

```bash
rm $(which infracost)
rm -rf "$HOME/Library/Application Support/infracost"
```

On Linux:

```bash
rm $(which infracost)
rm -rf "${XDG_CONFIG_HOME:-$HOME/.config}/infracost"
```

On Windows (PowerShell):

```powershell
Remove-Item (Get-Command infracost).Source
Remove-Item -Recurse "$env:APPDATA\infracost"
```

### Building locally

If you prefer to build from source:

1. `make build`
2. `./bin/infracost help`

## Usage

### Login

Before running any commands, authenticate with Infracost:

```bash
infracost auth login
```

This opens a browser-based login flow (PKCE). The resulting token is cached locally so you only need to log in once. If
you don't have access to a browser or localhost, use the device flow instead:

```bash
infracost auth login --oauth-use-device-flow
```

For non-interactive environments (CI/CD), set the `INFRACOST_CLI_AUTHENTICATION_TOKEN` environment variable to a
service account token or personal access token instead of using the login command.

### Setup

Once logged in, the interactive setup wizard walks you through configuring AI coding agents, your IDE, and CI:

```bash
infracost setup
```

### Scan

```bash
infracost scan /path/to/directory
```

The target must be a directory. If no argument is given, it defaults to the current working directory. The CLI will
auto-detect the IaC type from the directory contents, or you can configure projects explicitly via an `infracost.yml`
config file.

### Inspect

View a summary of the most recent scan results without re-running analysis:

```bash
infracost inspect --summary
```

### Plugins

Plugins are downloaded automatically from the plugin Infracost releases when you run the CLI. Parser plugins are ensured up front; provider plugins are downloaded on demand when a scan needs them. No manual setup is required.

#### Version Pinning

By default, the CLI downloads the latest version of each plugin. You can pin individual plugins to a specific version using environment variables:

- `INFRACOST_CLI_PLUGIN_TERRAFORM_VERSION` — pin the Terraform parser plugin version
- `INFRACOST_CLI_PLUGIN_TERRAGRUNT_VERSION` — pin the Terragrunt parser plugin version
- `INFRACOST_CLI_PLUGIN_CLOUDFORMATION_VERSION` — pin the CloudFormation parser plugin version
- `INFRACOST_CLI_PLUGIN_CISCOSTACKS_VERSION` — pin the CiscoStacks parser plugin version
- `INFRACOST_CLI_PLUGIN_AWS_VERSION` — pin the AWS provider plugin version
- `INFRACOST_CLI_PLUGIN_GOOGLE_VERSION` — pin the Google provider plugin version
- `INFRACOST_CLI_PLUGIN_AZURE_VERSION` — pin the Azure provider plugin version

The older parser/provider version environment variables are still accepted as fallbacks.

#### Updates

Plugins auto-update by default. Set `INFRACOST_CLI_PLUGIN_AUTO_UPDATE=false` to disable automatic plugin updates. When disabled, the CLI uses an existing flat-installed plugin binary if one exists, and only downloads from the plugin Infracost releases if the binary is missing.

Set `INFRACOST_CLI_PLUGIN_BASE_URL` to override the plugin Infracost releases URL. Use `--debug` to show plugin download URLs and other debug logs.

To update the CLI itself, you can use the `update` command. This updates the CLI binary by downloading the latest CLI archive from the Infracost releases bucket. Note that this does not update plugins, which are managed separately as described above.

#### Local Plugin Overrides

If you are developing plugins locally, you can bypass the download mechanism entirely by pointing the CLI at a flat directory containing your local plugin builds:

```bash
export INFRACOST_CLI_PLUGIN_DIR=/path/to/plugins
```

The directory should contain plugin binaries side by side, for example:

```text
/path/to/plugins/infracost-plugin-terraform
/path/to/plugins/infracost-plugin-terragrunt
/path/to/plugins/infracost-plugin-cloudformation
/path/to/plugins/infracost-plugin-ciscostacks
/path/to/plugins/infracost-plugin-aws
/path/to/plugins/infracost-plugin-google
/path/to/plugins/infracost-plugin-azure
```

When `INFRACOST_CLI_PLUGIN_DIR` is set, the CLI uses that directory as-is and skips plugin downloads.

## Bugs and feedback

If you run into any issues or have feedback, please open a thread in [GitHub Discussions](https://github.com/infracost/infracost/discussions).

## Contributing

We ❤️ contributions big or small. Please start by opening a thread in [GitHub Discussions](https://github.com/infracost/infracost/discussions) to discuss your idea before submitting a PR.
