# cli

> :construction: **Work in progress**
>
> This repository is under active development. Things may change quickly, break, or disappear entirely.

## Prerequisites

### GitHub Access

Plugins are hosted as GitHub release assets on this (currently private) repository. The CLI needs a GitHub token to 
download them. It checks for a token in the following order:

1. `GH_TOKEN` environment variable
2. `GITHUB_TOKEN` environment variable
3. `gh auth token` (if the [GitHub CLI](https://cli.github.com/) is installed and authenticated)

### Plugins

Plugins are downloaded automatically from the manifest when you run the CLI. No manual setup is required beyond having
GitHub access configured above.

#### Version Pinning

By default, the CLI downloads the latest version of each plugin. You can pin to a specific version using environment 
variables:

- `INFRACOST_CLI_PARSER_PLUGIN_VERSION` — pin the parser plugin version
- `INFRACOST_CLI_PROVIDER_PLUGIN_AWS_VERSION` — pin the AWS provider plugin version
- `INFRACOST_CLI_PROVIDER_PLUGIN_AZURE_VERSION` — pin the Azure provider plugin version
- `INFRACOST_CLI_PROVIDER_PLUGIN_GOOGLE_VERSION` — pin the Google provider plugin version

#### Auto-Update

Set `INFRACOST_CLI_PLUGIN_AUTO_UPDATE=false` to disable automatic updates. When disabled, the CLI uses the latest cached
version if one exists, and only downloads from the manifest if no cached version is found.

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

## Running the CLI

1. `make build`
2. `./bin/infracost help`

### Scan

```bash
./bin/infracost scan /path/to/directory
```

The target must be a directory. If no argument is given, it defaults to the current working directory. The CLI will 
auto-detect the IaC type from the directory contents, or you can configure projects explicitly via an `infracost.yml` 
config file.
