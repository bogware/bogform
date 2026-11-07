# Bogform

Bogform is a cheap, secure, and friendly way to manage your infrastructure as code, without paying
for a hosted platform. It's a fork of [OpenTofu](https://github.com/opentofu/opentofu) that keeps the
same language and workflow, so existing projects move over by swapping the executable.

[Documentation](https://bogware.github.io/bogform/docs/) |
[Install](https://bogware.github.io/bogform/docs/intro/install/) |
[Moving from OpenTofu or Terraform](https://bogware.github.io/bogform/docs/intro/migration/bogform/)

```shell
bogform init
bogform plan
bogform apply
```

## Ways to run it

- **Local:** run the `bogform` command on your own machine. `bogform ui` shows your pipeline reports
  in a local dashboard, with nothing to host.
- **Repository:** plan on pull requests and apply on merge or on demand, using GitHub Actions, GitLab
  CI, Azure Pipelines, or Bitbucket Pipelines. `bogform ci init` writes the pipeline, signed in to AWS
  or Azure with OIDC, and a [`BOGFORM.md`](templates/BOGFORM.md) that explains the setup to people
  and coding agents.
- **Self-hosted dashboard:** an optional web app that you deploy to your own AWS account, using
  CloudFront, AWS Lambda, and any Postgres database. It shows your runs and drift, sends alerts to
  Slack, Teams, email or a webhook, and starts runs in your existing pipelines, so there are no runners
  to host.

## Key features

- **Infrastructure as code:** describe your infrastructure in configuration that you can version,
  review, and reuse.
- **Execution plans:** see exactly what will change before anything does.
- **Resource graph:** changes are ordered by their dependencies and run in parallel where possible.
- **Existing providers and modules:** use the providers and modules from the OpenTofu Registry.
- **State encryption:** encrypt state and plan files with a key provider you control. The passphrase
  can come from an environment variable, your OS keychain, AWS SSM or Azure Key Vault.
- **State in your repository:** the `git` backend keeps encrypted state on a branch, with
  `bogform state history` and `state restore` to undo a bad change.
- **Safer pull requests:** plans list destroys first and run built-in checks for common AWS and Azure
  mistakes, applies refuse destroys unless a person allows them, and plans can include a monthly cost
  estimate.
- **Easy to adopt:** `bogform migrate tfc` leaves HCP Terraform, `bogform adopt` writes import blocks
  for resources you already have, `bogform bootstrap` sets up pipeline access to AWS or Azure, and
  `bogform doctor` checks the result.

## Install

```shell
brew install --cask bogware/tap/bogform                                          # macOS
scoop bucket add bogware https://github.com/bogware/scoop-bucket; scoop install bogform  # Windows
curl -fsSL https://raw.githubusercontent.com/bogware/bogform/main/scripts/install.sh | sh    # Linux and others
```

See [Installing Bogform](https://bogware.github.io/bogform/docs/intro/install/) for winget, packages
and containers.

## Compatibility

Bogform reads existing `.tf` and `.tofu` files, `TF_*` environment variables, state, and lock files.
It adds `.bog` files and `BOG_*` variables, which take priority. The HCP Terraform `cloud` block and
the `remote` backend aren't supported, and Bogform explains how to move your state if it finds them.

## Contributing and security

- To report a bug or suggest a feature, [open an issue](https://github.com/bogware/bogform/issues).
- To contribute, read the [contribution guide](CONTRIBUTING.md).
- To report a vulnerability, follow the [security policy](SECURITY.md).

## License

Bogform is licensed under the [Mozilla Public License 2.0](LICENSE). It's derived from OpenTofu, which
in turn derives from Terraform as it was licensed under the MPL 2.0. Copyright notices for those
projects are kept in the source files. Bogform isn't affiliated with or endorsed by the OpenTofu
project, the Linux Foundation, or HashiCorp.
