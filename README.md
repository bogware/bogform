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

## Status

Bogform is in early development. The rename and cleanup are underway, and these ways of running it
are planned:

- **Local:** run the `bogform` command on your own machine. This works today.
- **Repository (planned):** plan on pull requests and apply on merge, using GitHub Actions, GitLab CI,
  Azure Pipelines, or Bitbucket Pipelines, with state in low-cost object storage.
- **Self-hosted web (planned):** a dashboard you deploy to your own AWS account for your organization,
  using CloudFront, AWS Lambda, and any Postgres database. It starts runs in your existing pipelines,
  so there are no runners to host.

## Key features

- **Infrastructure as code:** describe your infrastructure in configuration that you can version,
  review, and reuse.
- **Execution plans:** see exactly what will change before anything does.
- **Resource graph:** changes are ordered by their dependencies and run in parallel where possible.
- **Existing providers and modules:** use the providers and modules from the OpenTofu Registry.
- **State encryption:** encrypt state and plan files with a key provider you control.

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
