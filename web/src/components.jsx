// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

export function navigate(href) {
  window.history.pushState(null, '', href);
  window.dispatchEvent(new PopStateEvent('popstate'));
  window.scrollTo(0, 0);
}

export function Link({ href, children, current, className }) {
  const onClick = (event) => {
    // Let the browser handle opening in a new tab or window.
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
    event.preventDefault();
    navigate(href);
  };
  return (
    <a href={href} className={className} aria-current={current ? 'page' : undefined} onClick={onClick}>
      {children}
    </a>
  );
}

export function Loading() {
  return (
    <p className="loading" role="status">
      Loading…
    </p>
  );
}

export function ErrorMessage({ error, onRetry }) {
  return (
    <div className="notice notice-error" role="alert">
      <p>{error.message}</p>
      {onRetry && (
        <button type="button" className="button" onClick={onRetry}>
          Try again
        </button>
      )}
    </div>
  );
}

const statuses = {
  no_changes: { label: 'No changes', tone: 'good' },
  changes: { label: 'Changes planned', tone: 'info' },
  applied: { label: 'Applied', tone: 'good' },
  drifted: { label: 'Drifted', tone: 'warn' },
  failed: { label: 'Failed', tone: 'bad' },
};

export function StatusBadge({ status }) {
  const known = statuses[status] ?? { label: status, tone: 'info' };
  return <span className={`badge badge-${known.tone}`}>{known.label}</span>;
}

const units = [
  ['day', 86400],
  ['hour', 3600],
  ['minute', 60],
];

export function TimeAgo({ value }) {
  if (!value) return <span>Never</span>;
  const date = new Date(value);
  const seconds = Math.round((date.getTime() - Date.now()) / 1000);
  const format = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
  let text = 'just now';
  for (const [unit, size] of units) {
    if (Math.abs(seconds) >= size) {
      text = format.format(Math.round(seconds / size), unit);
      break;
    }
  }
  return (
    <time dateTime={date.toISOString()} title={date.toLocaleString()}>
      {text}
    </time>
  );
}

export function Counts({ counts }) {
  const parts = [
    ['+', counts.add, 'to add', 'add'],
    ['~', counts.change, 'to change', 'change'],
    ['±', counts.replace, 'to replace', 'replace'],
    ['−', counts.destroy, 'to destroy', 'destroy'],
  ].filter(([, count]) => count > 0);
  if (parts.length === 0) return <span className="muted">No changes</span>;
  return (
    <span className="counts">
      {parts.map(([symbol, count, label, kind]) => (
        <span key={kind} className={`count count-${kind}`} title={`${count} ${label}`}>
          <span aria-hidden="true">{symbol}</span>
          {count}
          <span className="visually-hidden"> {label}</span>
        </span>
      ))}
    </span>
  );
}

export function workspaceName(item) {
  const directory = item.directory === '.' ? '' : ` / ${item.directory}`;
  return `${item.repository}${directory}`;
}

export function RunsTable({ runs, showWorkspace }) {
  if (runs.length === 0) {
    return <p className="empty">No runs yet.</p>;
  }
  return (
    <div className="table-scroll">
      <table>
        <thead>
          <tr>
            {showWorkspace && <th scope="col">Workspace</th>}
            <th scope="col">Run</th>
            <th scope="col">Status</th>
            <th scope="col">Changes</th>
            <th scope="col">Branch</th>
            <th scope="col">Finished</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => (
            <tr key={run.id}>
              {showWorkspace && (
                <td>
                  <Link href={`/workspaces/${run.workspace_id}`}>{workspaceName(run)}</Link>
                  {run.workspace !== 'default' && <span className="muted"> ({run.workspace})</span>}
                </td>
              )}
              <td className="nowrap">
                <Link href={`/runs/${run.id}`}>
                  {run.kind} #{run.id}
                </Link>
              </td>
              <td>
                <StatusBadge status={run.status} />
              </td>
              <td>
                <Counts counts={run.counts} />
              </td>
              <td className="mono">{run.branch || '—'}</td>
              <td>
                <TimeAgo value={run.finished_at} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
