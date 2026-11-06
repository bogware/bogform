// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

import { useEffect, useState } from 'react';
import { api, useApi } from '../api.js';
import { ErrorMessage, Link, Loading, RunsTable, StatusBadge, TimeAgo, workspaceName } from '../components.jsx';

const platformNames = { github: 'GitHub Actions', gitlab: 'GitLab CI', azure: 'Azure Pipelines', bitbucket: 'Bitbucket Pipelines' };

function StartRun({ id, workspace }) {
  const [state, setState] = useState({ phase: 'idle' });

  const start = async () => {
    setState({ phase: 'starting' });
    try {
      await api(`/workspaces/${id}/runs`, { method: 'POST' });
      setState({ phase: 'started' });
    } catch (error) {
      setState({ phase: 'failed', message: error.message });
    }
  };

  if (state.phase === 'confirm' || state.phase === 'starting') {
    return (
      <div className="confirm" role="group" aria-label="Confirm starting a pipeline">
        <p>
          Start the {platformNames[workspace.platform]} pipeline on <code>{workspace.default_ref}</code>? Its approval
          rules still apply.
        </p>
        <button type="button" className="button button-primary" onClick={start} disabled={state.phase === 'starting'}>
          {state.phase === 'starting' ? 'Starting…' : 'Start pipeline'}
        </button>{' '}
        <button type="button" className="button" onClick={() => setState({ phase: 'idle' })} disabled={state.phase === 'starting'}>
          Cancel
        </button>
      </div>
    );
  }
  return (
    <div>
      <button type="button" className="button button-primary" onClick={() => setState({ phase: 'confirm' })}>
        Start pipeline
      </button>
      {state.phase === 'started' && (
        <p className="notice notice-good" role="status">
          The pipeline has started. Its result appears here when it finishes.
        </p>
      )}
      {state.phase === 'failed' && (
        <p className="notice notice-error" role="alert">
          {state.message}
        </p>
      )}
    </div>
  );
}

export default function WorkspaceDetail({ id }) {
  const workspace = useApi(`/workspaces/${id}`);
  const [cursor, setCursor] = useState('');
  const page = useApi(`/workspaces/${id}/runs${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''}`);
  const [runs, setRuns] = useState([]);

  useEffect(() => {
    if (page.data) {
      setRuns((previous) => (cursor ? [...previous, ...page.data.runs] : page.data.runs));
    }
  }, [page.data, cursor]);

  if (workspace.loading && !workspace.data) return <Loading />;
  if (workspace.error) return <ErrorMessage error={workspace.error} onRetry={workspace.reload} />;

  const item = workspace.data.workspace;
  return (
    <>
      <p className="breadcrumb">
        <Link href="/workspaces">Workspaces</Link>
      </p>
      <div className="page-heading">
        <h1>{workspaceName(item)}</h1>
        {workspace.data.can_start_run && <StartRun id={id} workspace={item} />}
      </div>
      <dl className="facts">
        <div>
          <dt>Workspace</dt>
          <dd className="mono">{item.workspace}</dd>
        </div>
        <div>
          <dt>Platform</dt>
          <dd>{platformNames[item.platform] ?? item.platform}</dd>
        </div>
        <div>
          <dt>Last run</dt>
          <dd>
            <StatusBadge status={item.last_status} />
          </dd>
        </div>
        <div>
          <dt>Drift</dt>
          <dd>
            {item.drift_status === 'drifted' ? (
              <StatusBadge status="drifted" />
            ) : item.drift_checked_at ? (
              <>
                None, checked <TimeAgo value={item.drift_checked_at} />
              </>
            ) : (
              'Not checked yet'
            )}
          </dd>
        </div>
        <div>
          <dt>Last apply</dt>
          <dd>
            <TimeAgo value={item.last_apply_at} />
          </dd>
        </div>
      </dl>
      <section>
        <h2>Runs</h2>
        {page.error ? <ErrorMessage error={page.error} onRetry={page.reload} /> : <RunsTable runs={runs} />}
        {page.data?.next_cursor && (
          <button type="button" className="button load-more" disabled={page.loading} onClick={() => setCursor(page.data.next_cursor)}>
            {page.loading ? 'Loading…' : 'Show more'}
          </button>
        )}
      </section>
    </>
  );
}
