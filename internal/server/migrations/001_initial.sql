-- Copyright (c) The Bogform Authors
-- SPDX-License-Identifier: MPL-2.0

CREATE TABLE ingest_tokens (
    id bigserial PRIMARY KEY,
    name text NOT NULL UNIQUE,
    repository_scope text NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at timestamptz
);

CREATE TABLE workspaces (
    id bigserial PRIMARY KEY,
    platform text NOT NULL,
    repository text NOT NULL,
    directory text NOT NULL,
    name text NOT NULL,
    -- Status comes from runs outside pull requests once there has been one, because a plan on a pull
    -- request says nothing about what's actually deployed.
    last_status text NOT NULL,
    last_kind text NOT NULL,
    status_from_mainline boolean NOT NULL DEFAULT false,
    last_run_at timestamptz NOT NULL,
    last_apply_at timestamptz,
    drift_status text NOT NULL DEFAULT '',
    drift_checked_at timestamptz,
    UNIQUE (platform, repository, directory, name)
);

CREATE TABLE runs (
    id bigserial PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    kind text NOT NULL,
    status text NOT NULL,
    branch text NOT NULL DEFAULT '',
    commit_sha text NOT NULL DEFAULT '',
    pull_request text NOT NULL DEFAULT '',
    run_url text NOT NULL DEFAULT '',
    bogform_version text NOT NULL DEFAULT '',
    counts jsonb NOT NULL,
    changes jsonb NOT NULL,
    outputs_changed jsonb,
    error text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX runs_by_workspace ON runs (workspace_id, finished_at DESC, id DESC);
CREATE INDEX runs_by_time ON runs (finished_at DESC, id DESC);

CREATE TABLE audit_log (
    id bigserial PRIMARY KEY,
    at timestamptz NOT NULL DEFAULT now(),
    actor text NOT NULL,
    action text NOT NULL,
    target text NOT NULL,
    detail jsonb
);

CREATE INDEX audit_log_by_time ON audit_log (at DESC);
