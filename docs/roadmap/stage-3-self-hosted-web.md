# Stage 3: Self-hosted web dashboard

**Goal:** an organization can deploy its own Bogform dashboard to its own AWS account for a few dollars a
month or less. It shows what pipelines did and what's drifted, and it can start a run in the pipeline.
It doesn't run Bogform or hold cloud credentials, so there are no runners to host or secure.

## Architecture

```
Browser ── CloudFront ─┬─ /*      ── S3 (private, static UI)
                       └─ /api/*  ── Lambda (Go, function URL)
                                       │
Pipelines ── bogform ci ── report ─────┤
                                       ├── Postgres (any connection string)
                                       └── Platform API (start a pipeline run)
```

## Design principles

- **Costs nothing when idle.** Lambda and CloudFront scale to zero. Postgres is the only fixed cost, and
  any connection string works, so teams can use a serverless free tier or a small instance they have.
- **One binary, several hosts.** The same HTTP handler runs on Lambda and as `bogform-server serve`, which
  is also how it's developed and tested.
- **Holds no cloud credentials and runs no infrastructure code.** The worst outcome of a compromised
  dashboard is misleading information or an unwanted pipeline run, which still has to pass the pipeline's
  own approvals. Plan text isn't stored, because reports hold no attribute values.
- **Secure by default.** Signed, HttpOnly sessions. OIDC with PKCE, state, and nonce. Origin checks on
  requests that change data. Hashed ingest tokens. Security headers, including a strict CSP.

## Deliverables

### Server (`cmd/bogform-server`, `internal/server`)

- Automatic database migrations.
- `POST /api/v1/reports`: pipelines send reports, using a hashed ingest token.
- Sign-in with any OIDC provider. GitHub's OAuth, which isn't OIDC, works through a small adapter.
- Roles: `viewer` can read, and `operator` can also start runs. Roles come from an allowlist of email
  addresses and domains.
- Read APIs for overview, workspaces, and runs.
- `POST /api/v1/workspaces/{id}/runs`: starts a pipeline on GitHub, GitLab, Azure, or Bitbucket, using
  credentials configured on the server. Every run started is recorded in an audit log.
- Admin commands: `migrate`, `create-token`, `list-tokens`, `revoke-token`.

### CLI

- `bogform ci` sends the report if `BOG_SERVER_URL` and `BOG_SERVER_TOKEN` are set. A failure is a warning
  and never fails the pipeline.

### UI (`web/`)

- A small static React app with no runtime dependencies beyond React: overview, workspaces, a workspace's
  run history with its changes, and a button to start a run.
- Light and dark themes. It works on phones and meets basic accessibility expectations.

### Deployment (`deploy/aws`)

- A Bogform module that creates everything above, with least-privilege IAM and secrets in SSM Parameter
  Store. A local development setup with Docker Compose.

## Testing

- Handler tests against a real Postgres in Docker, including authorization for each route.
- A full OIDC sign-in against a fake identity provider.
- End-to-end: the real `bogform ci` sends a report to the real server, and the API returns it.
- UI build and browser checks against the running server.
- `bogform validate` on the deployment module.

## Out of scope

- Multi-tenancy, hosted runners, per-workspace permissions, and storing state or plans.
