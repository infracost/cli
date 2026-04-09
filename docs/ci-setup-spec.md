# `infracost ci setup`

**Linear:** [DEV-91](https://linear.app/infracost/issue/DEV-91/infracost-ci-setup)
**Project:** CLI 2.0 | **Estimate:** 2 Points

A command that automatically sets up CI integration for Infracost. See `infracost agent setup` and `infracost ide setup` for prior art.

## Modes

### Default flow (app integration)

The default flow opens the dashboard to connect the repo via the app integration. No YAML or secrets to manage.

```
$ infracost ci setup

Scanning repository
✔  GitHub repository  acme-corp/platform-infra
✔  VCS provider     GitHub
✔  Infracost org    acme-corp

  The recommended way to set up Infracost is the app integration.

  It works with GitHub, GitLab, and Azure Repos — no YAML or
  secrets to manage. Infracost handles everything automatically.

  Opening your dashboard to connect this repository:
  https://dashboard.infracost.io/org/acme-corp/repos

✔  Browser opened

  Once connected, Infracost will comment on every PR automatically.

  Need CI pipeline control instead?  infracost ci setup --ci-pipeline
```

### `--ci-pipeline` flag

Creates `.github/workflows/infracost.yml` and sets the `INFRACOST_API_KEY` secret via `gh secret set`.

```
$ infracost ci setup --ci-pipeline
Scanning repository
✔  GitHub repository  acme-corp/platform-infra
✔  GitHub Actions     .github/workflows/ detected
✔  Infracost API key  ready (from INFRACOST_API_KEY)
✔  gh CLI             authenticated as acme-rafa

  This will:
  →  Create  .github/workflows/infracost.yml
  →  Set     INFRACOST_API_KEY secret on acme-corp/platform-infra

  Ready? [Y/n] y

✔  Validated API key (org: acme-corp)
✔  Created .github/workflows/infracost.yml
✔  Set INFRACOST_API_KEY secret (via gh secret set)

  Done. Push this commit to see Infracost on your next PR:

  git add .github/workflows/infracost.yml
  git commit -m "chore: add Infracost CI integration"
  git push

  Cost estimates will appear as a PR comment
  https://github.com/acme-corp/platform-infra/compare

!  Using CI pipeline mode. For easier/automatic setup of
   additional repos, try the app integration:
   https://dashboard.infracost.io/org/acme-corp/repos
```

## Flags

- `--ci-pipeline` — Use CI pipeline mode instead of app integration
- `--yes` — Skip confirmation prompts for non-interactive scripting. Exits with non-zero code on failure. Useful for rolling out across multiple repos (e.g. `infracost ci setup --ci-pipeline --yes`).

## Fallback scenarios

### Already set up

```
✔  GitHub  acme-corp/platform-infra
✔  App integration already connected

  This repository is already sending PR cost estimates.
  To manage settings: https://dashboard.infracost.io/org/acme-corp/repos
```

### `gh` not installed or doesn't have permission

```
✔  GitHub repository  acme-corp/platform-infra
!  gh CLI             not found — secret will need to be set manually

✔  Created .github/workflows/infracost.yml

  One manual step remaining:
  Run this to add the secret via gh CLI:

  gh secret set INFRACOST_API_KEY --body "$INFRACOST_API_KEY" \
      --repo acme-corp/platform-infra

  Or add it in GitHub: https://github.com/acme-corp/platform-infra/settings/secrets/actions/new
```

### Workflow already exists

```
✔  GitHub repository  acme-corp/platform-infra
!  Infracost workflow already exists  .github/workflows/infracost.yml

  ? What would you like to do?

  ❯ Update workflow to latest recommended config
    View what would change (diff)
    Cancel
```

### Non-GitHub CI provider (with `--ci-pipeline`)

```
✔  Git repository    acme-corp/platform-infra
✗  CI provider       GitLab CI detected — GitHub Actions only for now

  Run `infracost ci setup` to use the GitLab app integration

  For manual CI integrations with GitLab, see the manual GitLab setup guide:
  https://www.infracost.io/docs/<whatever the path is>
```

### No `INFRACOST_API_KEY` set

CI pipeline setup is currently early access and requires whitelisted permission. If no API key is found, direct users to contact the sales team.

```
✔  GitHub repository  acme-corp/platform-infra
✗  Infracost API key  not found

  CI pipeline setup is currently in early access.
  To get access, please contact a sales representative.

  Already have a key? Set it as an environment variable:
  export INFRACOST_API_KEY=<your-key>
```

## Workflow templates

The `--ci-pipeline` flow should generate two workflow files. These are based on the existing `infracost/actions/diff` and `infracost/actions/scan` composite actions.

### `.github/workflows/infracost-diff.yml`

Runs on PRs to produce cost diff comments.

```yaml
name: Infracost Diff

on:
  pull_request:
    types: [opened, synchronize, reopened, closed]
  workflow_dispatch:
    inputs:
      pr-number:
        description: "Pull request number to scan"
        required: true
        type: number

permissions:
  contents: read
  pull-requests: write

jobs:
  infracost-diff:
    runs-on: ubuntu-latest
    steps:
      - name: Get PR details
        id: pr
        env:
          GH_TOKEN: ${{ github.token }}
          PR_NUMBER: ${{ inputs.pr-number || github.event.pull_request.number }}
        run: |
          if [ -n "${{ inputs.pr-number }}" ]; then
            BASE_REF=$(gh pr view "$PR_NUMBER" --repo "$GITHUB_REPOSITORY" --json baseRefName -q .baseRefName)
            HEAD_REF=$(gh pr view "$PR_NUMBER" --repo "$GITHUB_REPOSITORY" --json headRefName -q .headRefName)
          else
            BASE_REF="${{ github.event.pull_request.base.ref }}"
            HEAD_REF="${{ github.event.pull_request.head.ref }}"
          fi
          echo "base-ref=${BASE_REF}" >> $GITHUB_OUTPUT
          echo "head-ref=${HEAD_REF}" >> $GITHUB_OUTPUT
          echo "pr-number=${PR_NUMBER}" >> $GITHUB_OUTPUT

      - name: Checkout base branch
        if: github.event.action != 'closed'
        uses: actions/checkout@v4
        with:
          ref: ${{ steps.pr.outputs.base-ref }}
          path: base

      - name: Checkout head branch
        if: github.event.action != 'closed'
        uses: actions/checkout@v4
        with:
          ref: ${{ steps.pr.outputs.head-ref }}
          path: head

      - name: Run Infracost Diff
        uses: infracost/actions/diff@<pinned-sha>
        with:
          api-key: ${{ secrets.INFRACOST_API_KEY }}
          base-path: ${{ github.event.action != 'closed' && 'base' || '' }}
          head-path: ${{ github.event.action != 'closed' && 'head' || '' }}
          pr-number: ${{ steps.pr.outputs.pr-number }}
```

### `.github/workflows/infracost-scan.yml`

Runs on push to main to upload baseline cost data.

```yaml
name: Infracost Scan

on:
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  infracost-scan:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Run Infracost Scan
        uses: infracost/actions/scan@<pinned-sha>
        with:
          api-key: ${{ secrets.INFRACOST_API_KEY }}
          path: .
```

> **Note:** The `<pinned-sha>` should be resolved at generation time to the latest release SHA from `infracost/actions`. The actions themselves handle downloading and running the `infracost-scanner` binary. The `api-key` input is passed through as `INFRACOST_CLI_AUTHENTICATION_TOKEN` internally by the actions.

## Notes

- The CLI reads the API key from `INFRACOST_CLI_AUTHENTICATION_TOKEN` (see `pkg/auth/config.go:73`). We're keeping this env var name as-is to avoid a breaking change in the composite actions (`infracost/actions/diff` and `infracost/actions/scan`), which already map their `api-key` input to `INFRACOST_CLI_AUTHENTICATION_TOKEN`. User-facing messaging in `ci setup` should refer to `INFRACOST_API_KEY` — this is the name of the GitHub Actions secret users set, and the actions handle the mapping internally.
- Organization lookup follows the same pattern as `whoami` (see `internal/cmds/whoami.go`): authenticate, then call `CurrentUser` to get the user's organizations. If the user belongs to multiple organizations, `INFRACOST_CLI_ORG_ID` (see `internal/config/config.go:33`) must be set to disambiguate. TODO: [DEV-232](https://linear.app/infracost/issue/DEV-232/add-infracost-orgs-subbcommand) adds `infracost org` subcommands with interactive org selection and per-repo overrides — once that lands, `ci setup` should use the same flow to prompt the user to pick an org interactively instead of requiring the env var.

## Obtaining an API key

Users can create an API key at `https://dashboard.infracost.io/org/<org-slug>/settings/cli-tokens`. The `ci setup` command should generate this link using the resolved org slug.

Important:
- Users **must** select a token from the **"CLI and CI/CD tokens (Preview)"** section — legacy tokens from the "CLI and CI/CD token (Legacy)" section will **not** work.
- The Preview section is only visible to whitelisted organizations. If a user cannot see it, they need to contact a sales representative to have it enabled for their org.