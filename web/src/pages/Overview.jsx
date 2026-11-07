// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

import { useApi } from '../api.js';
import { Counts, ErrorMessage, Link, Loading, RunsTable, StatusBadge, TimeAgo } from '../components.jsx';

function Stat({ label, value, tone, href }) {
  return (
    <Link href={href} className={`stat ${value > 0 && tone ? `stat-${tone}` : ''}`}>
      <span className="stat-value">{value}</span>
      <span className="stat-label">{label}</span>
    </Link>
  );
}

function GettingStarted({ local }) {
  if (local) {
    return (
      <section className="panel">
        <h2>No reports found</h2>
        <p>
          <code>bogform ui</code> shows the <code>.bogform-ci/report.json</code> files that <code>bogform ci</code> writes.
          Run <code>bogform ci plan</code> in a configuration directory, then reload this page.
        </p>
      </section>
    );
  }
  return (
    <section className="panel">
      <h2>Send your first report</h2>
      <ol className="steps">
        <li>
          Create a token on the server: <code>bogform-server create-token -name infra -repository 'acme/*'</code>
        </li>
        <li>
          Add these to your pipeline&apos;s secrets and variables: <code>BOG_SERVER_URL</code> and{' '}
          <code>BOG_SERVER_TOKEN</code>
        </li>
        <li>
          Run a pipeline that uses <code>bogform ci</code>. It shows up here when it finishes.
        </li>
      </ol>
    </section>
  );
}

// LastRun shows the latest run of one kind, linked to its details.
function LastRun({ run }) {
  if (!run) return <span className="muted">—</span>;
  return (
    <span className="last-run">
      <Link href={`/runs/${run.id}`}>
        <StatusBadge status={run.status} />
      </Link>
      <span className="muted">
        <TimeAgo value={run.finished_at} />
        {run.pull_request && <> · PR {run.pull_request}</>}
      </span>
    </span>
  );
}

function Repository({ repository }) {
  const id = `repo-${repository.platform}-${repository.repository}`.replace(/[^A-Za-z0-9_-]/g, '-');
  return (
    <section className="repository" aria-labelledby={id}>
      <h3 id={id}>{repository.repository}</h3>
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              <th scope="col">Directory</th>
              <th scope="col">Last plan</th>
              <th scope="col">Last apply</th>
              <th scope="col">Drift check</th>
              <th scope="col">Not applied</th>
            </tr>
          </thead>
          <tbody>
            {repository.workspaces.map((item) => (
              <tr key={item.id}>
                <td>
                  <Link href={`/workspaces/${item.id}`}>{item.directory}</Link>
                  {item.workspace !== 'default' && <span className="muted"> ({item.workspace})</span>}
                </td>
                <td>
                  <LastRun run={item.last_plan} />
                </td>
                <td>
                  <LastRun run={item.last_apply} />
                </td>
                <td>
                  <LastRun run={item.last_drift} />
                </td>
                <td>{item.open_changes ? <Counts counts={item.open_changes} /> : <span className="muted">—</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

export default function Overview({ local }) {
  const { data, error, loading, reload } = useApi('/overview');
  if (loading && !data) return <Loading />;
  if (error) return <ErrorMessage error={error} onRetry={reload} />;

  return (
    <>
      <div className="page-heading">
        <h1>Overview</h1>
        {local && (
          <button type="button" className="button" onClick={reload} disabled={loading}>
            {loading ? 'Reloading…' : 'Reload'}
          </button>
        )}
      </div>
      {data.workspaces === 0 ? (
        <GettingStarted local={local} />
      ) : (
        <>
          <section className="stats" aria-label="Summary">
            <Stat label="Workspaces" value={data.workspaces} href="/workspaces" />
            <Stat label="Failing" value={data.failing} tone="bad" href="/workspaces" />
            <Stat label="Drifted" value={data.drifted} tone="warn" href="/workspaces" />
            <Stat label="Runs in the last day" value={data.runs_today} href="/workspaces" />
          </section>
          <section>
            <h2>Repositories</h2>
            {(data.repositories ?? []).map((repository) => (
              <Repository key={`${repository.platform}:${repository.repository}`} repository={repository} />
            ))}
          </section>
          <section>
            <h2>Recent runs</h2>
            <RunsTable runs={data.recent_runs} showWorkspace />
          </section>
        </>
      )}
    </>
  );
}
