// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

import { useApi } from '../api.js';
import { Counts, ErrorMessage, Link, Loading, StatusBadge, TimeAgo, workspaceName } from '../components.jsx';

function actionLabel(actions) {
  const has = (name) => actions.includes(name);
  if (has('create') && has('delete')) return ['replace', 'Replace'];
  if (has('create')) return ['add', 'Create'];
  if (has('delete')) return ['destroy', 'Destroy'];
  if (has('update')) return ['change', 'Update'];
  if (has('read')) return ['read', 'Read'];
  return ['read', 'Import'];
}

function duration(run) {
  const seconds = Math.round((new Date(run.finished_at) - new Date(run.started_at)) / 1000);
  if (seconds < 60) return `${seconds}s`;
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

export default function RunDetail({ id }) {
  const { data: run, error, loading, reload } = useApi(`/runs/${id}`);
  if (loading && !run) return <Loading />;
  if (error) return <ErrorMessage error={error} onRetry={reload} />;

  return (
    <>
      <p className="breadcrumb">
        <Link href={`/workspaces/${run.workspace_id}`}>{workspaceName(run)}</Link>
      </p>
      <div className="page-heading">
        <h1>
          {run.kind} #{run.id}
        </h1>
        <StatusBadge status={run.status} />
      </div>
      {run.error && (
        <p className="notice notice-error" role="alert">
          {run.error}
        </p>
      )}
      <dl className="facts">
        <div>
          <dt>Changes</dt>
          <dd>
            <Counts counts={run.counts} />
          </dd>
        </div>
        <div>
          <dt>Branch</dt>
          <dd className="mono">{run.branch || '—'}</dd>
        </div>
        <div>
          <dt>Commit</dt>
          <dd className="mono">{run.commit ? run.commit.slice(0, 8) : '—'}</dd>
        </div>
        {run.pull_request && (
          <div>
            <dt>Pull request</dt>
            <dd>#{run.pull_request}</dd>
          </div>
        )}
        <div>
          <dt>Finished</dt>
          <dd>
            <TimeAgo value={run.finished_at} /> ({duration(run)})
          </dd>
        </div>
        <div>
          <dt>Bogform version</dt>
          <dd className="mono">{run.bogform_version}</dd>
        </div>
      </dl>
      {run.run_url && (
        <p>
          <a href={run.run_url} target="_blank" rel="noreferrer noopener">
            View the full log in your pipeline
          </a>
        </p>
      )}
      <section>
        <h2>Resources</h2>
        {run.changes?.length ? (
          <ul className="changes">
            {run.changes.map((change) => {
              const [tone, label] = actionLabel(change.actions);
              return (
                <li key={change.address}>
                  <span className={`action action-${tone}`}>{label}</span>
                  <code>{change.address}</code>
                </li>
              );
            })}
          </ul>
        ) : (
          <p className="empty">No resources changed.</p>
        )}
      </section>
      {run.outputs_changed?.length > 0 && (
        <section>
          <h2>Outputs changed</h2>
          <ul className="changes">
            {run.outputs_changed.map((name) => (
              <li key={name}>
                <code>{name}</code>
              </li>
            ))}
          </ul>
        </section>
      )}
    </>
  );
}
