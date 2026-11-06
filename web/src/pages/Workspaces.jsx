// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

import { useMemo, useState } from 'react';
import { useApi } from '../api.js';
import { ErrorMessage, Link, Loading, StatusBadge, TimeAgo, workspaceName } from '../components.jsx';

const severity = (workspace) => {
  if (workspace.last_status === 'failed') return 0;
  if (workspace.drift_status === 'drifted') return 1;
  return 2;
};

export default function Workspaces() {
  const { data, error, loading, reload } = useApi('/workspaces');
  const [filter, setFilter] = useState('');

  const workspaces = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    return (data?.workspaces ?? [])
      .filter((item) => !needle || `${item.repository} ${item.directory} ${item.workspace}`.toLowerCase().includes(needle))
      .sort((a, b) => severity(a) - severity(b));
  }, [data, filter]);

  if (loading && !data) return <Loading />;
  if (error) return <ErrorMessage error={error} onRetry={reload} />;

  return (
    <>
      <div className="page-heading">
        <h1>Workspaces</h1>
        <label className="filter">
          <span className="visually-hidden">Filter workspaces</span>
          <input type="search" placeholder="Filter" value={filter} onChange={(event) => setFilter(event.target.value)} />
        </label>
      </div>
      {workspaces.length === 0 ? (
        <p className="empty">{filter ? 'No workspaces match that filter.' : 'No pipeline has reported yet.'}</p>
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th scope="col">Repository</th>
                <th scope="col">Workspace</th>
                <th scope="col">Last run</th>
                <th scope="col">Drift</th>
                <th scope="col">Last apply</th>
                <th scope="col">Updated</th>
              </tr>
            </thead>
            <tbody>
              {workspaces.map((item) => (
                <tr key={item.id}>
                  <td>
                    <Link href={`/workspaces/${item.id}`}>{workspaceName(item)}</Link>
                  </td>
                  <td className="mono">{item.workspace}</td>
                  <td>
                    <StatusBadge status={item.last_status} />
                  </td>
                  <td>{item.drift_status === 'drifted' ? <StatusBadge status="drifted" /> : <span className="muted">—</span>}</td>
                  <td>
                    <TimeAgo value={item.last_apply_at} />
                  </td>
                  <td>
                    <TimeAgo value={item.last_run_at} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
