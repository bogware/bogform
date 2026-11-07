-- Copyright (c) The Bogform Authors
-- SPDX-License-Identifier: MPL-2.0

-- The overview finds each workspace's latest run of each kind.
CREATE INDEX runs_by_workspace_kind ON runs (workspace_id, kind, finished_at DESC, id DESC);
