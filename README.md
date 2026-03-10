# cli

> [!WARNING]
>
> This repository is in early alpha. Features may change and rough edges are expected.
> [Open a discussion thread](https://github.com/infracost/infracost/discussions) to report bugs or
> share feedback — it is genuinely appreciated.

Infracost estimates cloud costs from infrastructure as code, helping you catch cost surprises before they hit your bill.
It currently supports Terraform, Terragrunt, and CloudFormation.

## Installation

Download the latest release archive for your platform from the
[GitHub Releases page](https://github.com/infracost/cli/releases), then extract the binary and place it somewhere on
your `PATH`.

For example, on macOS (Apple Silicon):

```bash
# Download and extract
tar -xzf infracost-preview_0.0.2_darwin_arm64.tar.gz

# Move the binary onto your PATH
mv infracost-preview /usr/local/bin/infracost-preview
```

On Linux (amd64):

```bash
tar -xzf infracost-preview_0.0.2_linux_amd64.tar.gz
mv infracost-preview /usr/local/bin/infracost-preview
```

On Windows, download the `.zip` archive and extract it to a directory on your `PATH`.

Once installed, verify it works:

```bash
infracost-preview help
```

### Uninstalling

Remove the binary and the cached configuration/token data.

On macOS:

```bash
rm $(which infracost-preview)
rm -rf "$HOME/Library/Application Support/infracost"
```

On Linux:

```bash
rm $(which infracost-preview)
rm -rf "${XDG_CONFIG_HOME:-$HOME/.config}/infracost"
```

On Windows (PowerShell):

```powershell
Remove-Item (Get-Command infracost-preview).Source
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
infracost-preview login
```

This opens a browser-based login flow (PKCE). The resulting token is cached locally so you only need to log in once. If
you don't have access to a browser or localhost, use the device flow instead:

```bash
infracost-preview login --oauth-use-device-flow
```

For non-interactive environments (CI/CD), set the `INFRACOST_CLI_AUTHENTICATION_TOKEN` environment variable to a
service account token or personal access token instead of using the login command.

### Scan

```bash
infracost-preview scan /path/to/directory
```

The target must be a directory. If no argument is given, it defaults to the current working directory. The CLI will
auto-detect the IaC type from the directory contents, or you can configure projects explicitly via an `infracost.yml`
config file.

### Plugins

Plugins are downloaded automatically from the manifest when you run the CLI. No manual setup is required.

#### Version Pinning

By default, the CLI downloads the latest version of each plugin. You can pin to a specific version using environment
variables:

- `INFRACOST_CLI_PARSER_PLUGIN_VERSION` — pin the parser plugin version
- `INFRACOST_CLI_PROVIDER_PLUGIN_AWS_VERSION` — pin the AWS provider plugin version
- `INFRACOST_CLI_PROVIDER_PLUGIN_AZURE_VERSION` — pin the Azure provider plugin version
- `INFRACOST_CLI_PROVIDER_PLUGIN_GOOGLE_VERSION` — pin the Google provider plugin version

#### Updates

Plugins auto-update by default. Set `INFRACOST_CLI_PLUGIN_AUTO_UPDATE=false` to disable automatic plugin updates. When disabled, the CLI uses the latest cached version if one exists, and only downloads from the manifest if no cached version is found.

To update the CLI itself, you can use the `update` command. This will update the CLI binary by downloading the latest release from GitHub. Note that this does not update plugins, which are managed separately as described above.

#### Local Plugin Overrides

If you are developing plugins locally, you can bypass the download mechanism entirely by pointing the CLI at your local
builds:

```bash
# Parser
export INFRACOST_CLI_PARSER_PLUGIN=/path/to/bin/infracost-parser-plugin

# Providers
export INFRACOST_CLI_PROVIDER_PLUGIN_AWS=/path/to/bin/infracost-provider-plugin-aws
export INFRACOST_CLI_PROVIDER_PLUGIN_GOOGLE=/path/to/bin/infracost-provider-plugin-google
export INFRACOST_CLI_PROVIDER_PLUGIN_AZURERM=/path/to/bin/infracost-provider-plugin-azurerm
```

When a plugin path override is set, the CLI uses that binary directly and skips downloading for that plugin.
