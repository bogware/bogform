// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

import { useApi } from '../api.js';
import { ErrorMessage, Link, Loading, RunsTable } from '../components.jsx';

function Stat({ label, value, tone, href }) {
  return (
    <Link href={href} className={`stat ${value > 0 && tone ? `stat-${tone}` : ''}`}>
      <span className="stat-value">{value}</span>
      <span className="stat-label">{label}</span>
    </Link>
  );
}

function GettingStarted() {
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

export default function Overview() {
  const { data, error, loading, reload } = useApi('/overview');
  if (loading && !data) return <Loading />;
  if (error) return <ErrorMessage error={error} onRetry={reload} />;

  return (
    <>
      <h1>Overview</h1>
      {data.workspaces === 0 ? (
        <GettingStarted />
      ) : (
        <>
          <section className="stats" aria-label="Summary">
            <Stat label="Workspaces" value={data.workspaces} href="/workspaces" />
            <Stat label="Failing" value={data.failing} tone="bad" href="/workspaces" />
            <Stat label="Drifted" value={data.drifted} tone="warn" href="/workspaces" />
            <Stat label="Runs in the last day" value={data.runs_today} href="/workspaces" />
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
