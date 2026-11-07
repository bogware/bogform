# Changelog

## 1.16.0 (Unreleased)

BUG FIXES:

- `bogform ci init -git-state` sets `BOGFORM_STATE_PASSPHRASE` as well as `TF_VAR_state_passphrase`
  from the passphrase secret, so pipelines work with the `passphrase` key provider without editing.

## 1.15.0 (November 7, 2026)

The first stable release of Bogform. It follows 1.14.0-alpha2; there's no 1.14.0.

NEW FEATURES:

- `bogform migrate tfc` moves HCP Terraform and Terraform Enterprise workspaces to any backend. It
  copies the current state, encrypted and with its lineage intact, and reports each workspace's
  settings and variables with the matching `ci init` options. It only reads from HCP Terraform.
- `bogform adopt aws` (by tag or name prefix) and `bogform adopt azure` (by resource group) find
  existing resources and write `import` blocks. Import IDs that look like email addresses are left
  out, because plan output shows them.
- `bogform bootstrap aws` and `bogform bootstrap azure` write a configuration that gives pipelines a
  read-only plan identity and a default-branch-only apply identity for each repository.
- `bogform doctor` checks a configuration's setup, from its backend and passphrase to its pipeline
  and committed secrets, and says how to fix what it finds.
- `bogform ui` shows `bogform ci` reports in a local, read-only dashboard, with nothing to host.
- The new `passphrase` key provider reads the state passphrase from an environment variable, the OS
  keychain, AWS SSM Parameter Store or Azure Key Vault, and chains into `pbkdf2`. Switching to it
  doesn't re-encrypt existing state.
- `bogform state passphrase set` and `delete` manage the passphrase in the OS keychain.
- `bogform state history` and `bogform state restore` list and bring back earlier versions of the
  state, with the `git` backend, or `s3` with bucket versioning.
- `bogform state rekey` re-encrypts every workspace's state with the current method, to finish
  rotating a passphrase or key.
- `bogform ci apply` refuses plans that destroy or replace resources unless a person allows it, with
  `-allow-destroy`, `BOG_CI_ALLOW_DESTROY=true`, or on GitHub the `allow-destroy` label on the merged
  pull request. Generated pipelines have a matching input. Plan comments list destroys first.
- `bogform ci plan` and `apply` run built-in checks for common AWS and Azure mistakes, such as
  public storage, unencrypted disks, wildcard IAM policies and SSH open to the internet. They're
  warnings, and can be turned off with `-skip-checks`, `-checks=false`, or a `bogform:skip-checks`
  tag.
- `bogform ci plan -cost` and `ci init -cost` add a monthly cost estimate to plans, from the AWS and
  Azure price lists.
- The dashboard sends alerts to Slack, Microsoft Teams, email through SES, or a signed webhook, when
  an apply succeeds or fails, a plan fails, or drift starts or ends.
- The dashboard's overview lists every repository's directories, with their latest plan, apply and
  drift check, and puts what needs attention first.

ENHANCEMENTS:

- `bogform ci init` signs pipelines in to AWS or Azure with OIDC, on all four platforms, with
  `-aws-plan-role` and `-aws-apply-role` or `-azure-client-id` and related options.
- `bogform ci init` writes `BOGFORM.md`, a guide to the setup for people and coding agents, with its
  repository section filled in. Use `-bogform-md=false` to skip it. The template is
  `templates/BOGFORM.md`.
- Reports record who started a run and why, where the platform provides it.
- The error for `cloud` and `remote` blocks suggests `bogform migrate tfc`.
- Releases are published to Homebrew (`brew install --cask bogware/tap/bogform`), Scoop and
  winget, and there are install scripts that check the release's checksums and signature.
- Each release archive has an SPDX SBOM, and each release file a GitHub build attestation.
- Container images are multi-platform, built with GoReleaser 2.

BUG FIXES:

- `bogform ci` runs plans and applies with `-concise`, so pull request comments and reports leave
  out progress messages. These were noise, and showed every resource's ID, even an ID that's
  private, like an email address.

OTHER CHANGES:

- Removed OpenTofu's RFCs, which stay available upstream, and the experimental engine's workflow.

## 1.14.0-alpha2 (November 7, 2026)

ENHANCEMENTS:

- `bogform ci init` now takes `-apply-on`: `manual`, the default, plans on pull requests and applies only
  when someone runs the pipeline, and `merge` applies every merged change.
- `bogform ci init -git-state` sets up a pipeline for the `git` backend.
- `bogform ci init -environment=` leaves the environment out, for private GitHub repositories without
  a paid plan.
- Generated GitHub pipelines use current versions of `actions/checkout` and
  `aws-actions/configure-aws-credentials`.

BUG FIXES:

- The `bogform-server` archives now keep the dashboard UI's `assets` folder, so the UI loads.
- `bogform ci init` pins the full version, including any prerelease suffix.

## 1.14.0-alpha1 (November 6, 2026)

Bogform forked from OpenTofu at 1.14.0-dev. Entries below this section are inherited from OpenTofu and
use its name.

BREAKING CHANGES:

- The `cloud` block and the `remote` backend are no longer supported. Configurations that use them
  fail with steps for moving state to a supported backend.

NEW FEATURES:

- Added `bogform ci` to run Bogform from GitHub Actions, GitLab CI, Azure Pipelines, and Bitbucket
  Pipelines. It comments on pull requests with the plan, and `bogform ci init` adds a pipeline to your
  repository.
- Added the `git` backend, which stores encrypted state on a dedicated branch of a Git repository.
- Added an optional self-hosted dashboard, `bogform-server`, that shows pipeline runs and drift and can
  start pipelines. `deploy/aws` deploys it using CloudFront, AWS Lambda, and any Postgres database.

ENHANCEMENTS:

- The executable is now named `bogform`.
- Added `.bog`, `.bog.json`, `.bogtest.hcl`, `.bogtest.json`, and `.bogmock.hcl` files, which replace
  `.tofu` and `.tf` files with the same name.
- Added `BOG_*` environment variables, which override their `TF_*` and `TOFU_*` equivalents.
- Added `~/.bogformrc` and `bogform.rc` CLI configuration files.

## 1.14.0 (Unreleased)

UPGRADE NOTES:

- We are no longer producing official builds for 32-bit CPU architectures (`*_386` and `*_arm` platforms). ([#4530](https://github.com/opentofu/opentofu/pull/4530))

    If you are currently relying on our official releases of OpenTofu on one of these platforms then you will need to migrate to running OpenTofu on a 64-bit CPU architecture (`*_amd64` or `*_arm64` platforms) before upgrading from OpenTofu v1.13.

    Third parties may continue to offer their own OpenTofu builds targeting platforms that we don't officially support. This only affects the official packages published directly by the OpenTofu project in this repository's release artifacts.

- `provider_meta` blocks in `terraform` blocks are now just silently ignored by OpenTofu, behaving as if they were not declared at all. ([#4600](https://github.com/opentofu/opentofu/pull/4600))

ENHANCEMENTS:

- `tofu plan` no longer prints iterative warnings for multiple resources but instead it shows one warning with all of the affected resources. ([#4201](https://github.com/opentofu/opentofu/issues/4201))
- For provider installation, registry and network mirror sources can now optionally forward authentication credentials to the package download URL if the server explicitly opts in. Previously the package download requests were always made without any credentials. ([#4584](https://github.com/opentofu/opentofu/issues/4584))
- `mock_provider` now supports the `source` argument that can get a file or directory with specific provider overrides ([#4532](https://github.com/opentofu/opentofu/pull/4532))
- Module outputs now support specifying their type. ([#4645](https://github.com/opentofu/opentofu/pull/4645))
- The `tofu` cli will now read `*.tfrc` file to load its configuration from system standard directories. ([#4624](https://github.com/opentofu/opentofu/pull/4624))
- `override_data`/`override_resource` blocks may override non-computed fields in tofu test. ([#4777](https://github.com/opentofu/opentofu/pull/4777))
- `tofu version` now includes the list of modules and their versions. ([#3270](https://github.com/opentofu/opentofu/issues/3270))

BUG FIXES:

- `tofu fmt`: Fixed wrong resolution of paths when the working directory is a symlink; output now shows absolute file paths instead of giving error `Invalid file or directory path`. ([#3879](https://github.com/opentofu/opentofu/issues/3879))
- OpenTelemetry tracing support now honors the standard OpenTelemetry environment variable `OTEL_RESOURCE_ATTRIBUTES` for specifying arbitrary additional resource attribute values to be added to traces. Previously we only supported `OTEL_SERVICE_NAME` for overriding the `service.name` attribute. ([#4621](https://github.com/opentofu/opentofu/issues/4621))

## Previous Releases

For information on prior major and minor releases, refer to their changelogs:

- [v1.13](https://github.com/opentofu/opentofu/blob/v1.13/CHANGELOG.md)
- [v1.12](https://github.com/opentofu/opentofu/blob/v1.12/CHANGELOG.md)
- [v1.11](https://github.com/opentofu/opentofu/blob/v1.11/CHANGELOG.md)
- [v1.10](https://github.com/opentofu/opentofu/blob/v1.10/CHANGELOG.md)
- [v1.9](https://github.com/opentofu/opentofu/blob/v1.9/CHANGELOG.md)
- [v1.8](https://github.com/opentofu/opentofu/blob/v1.8/CHANGELOG.md)
- [v1.7](https://github.com/opentofu/opentofu/blob/v1.7/CHANGELOG.md)
- [v1.6](https://github.com/opentofu/opentofu/blob/v1.6/CHANGELOG.md)
