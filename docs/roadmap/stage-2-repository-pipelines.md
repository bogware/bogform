# Stage 2: Repository pipelines

**Goal:** a team can run Bogform from its repository, with no hosted platform, by committing one file.
Pull requests get a plan comment, merges apply, and every run has a machine-readable report.

## Design principles

- **Logic lives in the CLI.** All four platforms call `bogform ci`, so behavior is the same everywhere
  and can be tested without any platform. Pipeline files stay short.
- **No shared control plane.** Generated pipelines are self-contained files that the team owns.
- **Secure by default.** Applying from a pull request is refused unless explicitly allowed. The token
  used for comments isn't passed to providers. Git state refuses to store unencrypted data.
- **Failure of a convenience never fails the job.** A comment that can't be posted, such as on a pull
  request from a fork, is a warning.

## Deliverables

### `bogform ci` commands

| Command | Purpose |
|---|---|
| `ci plan` | Initialize, plan, publish a report, and comment on the pull request |
| `ci apply` | Initialize, plan, apply, and publish a report |
| `ci drift` | Run a refresh-only plan. Exits with 2 if real infrastructure has drifted |
| `ci init` | Write a pipeline file for GitHub Actions, GitLab CI, Azure Pipelines, or Bitbucket Pipelines |
| `ci context` | Show what Bogform detected about the current pipeline |

Comments are updated in place, so a pull request has one comment for each directory and workspace.
Each run writes `.bogform-ci/report.json`, which is the format that the Stage 3 dashboard consumes.

### Pipeline support

| Platform | Comments on | Identity for cloud access | Approval gate |
|---|---|---|---|
| GitHub Actions | Pull requests | OIDC | Environments |
| GitLab CI | Merge requests | ID tokens | Protected environments |
| Azure Pipelines | Pull requests | Workload identity | Environments |
| Bitbucket Pipelines | Pull requests | OIDC | Deployments |

A reusable `action.yml` at the repository root installs a verified release on GitHub Actions.

### State

- Object storage (`s3`, `gcs`, `azurerm`) stays the recommended default, with setup guidance.
- A new `git` backend stores encrypted state on a dedicated branch for small projects that want no
  cloud setup. Locks are branches created with a compare-and-swap push. It refuses to store
  unencrypted state unless `allow_unencrypted` is set.

### Cost control for this repository

Only Linux jobs run on pushes to `main`. macOS and Windows end-to-end tests, which use more Actions
minutes, run on release tags and on demand.

## Testing

- Unit tests for detection, reports, and each platform's comment API against a fake server.
- End-to-end tests that run the real binary against fake platform APIs.
- Backend conformance tests for the `git` backend against a local bare repository.
- A workflow that runs `bogform ci` through `action.yml` on GitHub Actions.

## Out of scope

- Hosted runners, policy engines, and cost estimation.
- Detecting which directories changed. Teams with several roots use one job for each directory.
