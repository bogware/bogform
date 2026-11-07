// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

import { useEffect, useState } from 'react';
import { api, useApi } from './api.js';
import Overview from './pages/Overview.jsx';
import Workspaces from './pages/Workspaces.jsx';
import WorkspaceDetail from './pages/WorkspaceDetail.jsx';
import RunDetail from './pages/RunDetail.jsx';
import { ErrorMessage, Link, Loading, navigate } from './components.jsx';

const authErrors = {
  'not-allowed': "Your account doesn't have access to this dashboard. Ask an administrator to add you.",
  denied: 'Sign-in was canceled.',
  expired: 'Your sign-in took too long. Try again.',
  invalid: "Sign-in couldn't be verified. Try again.",
  failed: "Sign-in didn't work. Try again, or ask an administrator to check the server's log.",
};

function useLocationPath() {
  const [path, setPath] = useState(window.location.pathname);
  useEffect(() => {
    const update = () => setPath(window.location.pathname);
    window.addEventListener('popstate', update);
    return () => window.removeEventListener('popstate', update);
  }, []);
  return path;
}

function SignIn() {
  const code = new URLSearchParams(window.location.search).get('auth_error');
  return (
    <main className="sign-in">
      <h1>Bogform</h1>
      <p>Sign in to see your infrastructure runs.</p>
      {code && (
        <p className="notice notice-error" role="alert">
          {authErrors[code] ?? authErrors.failed}
        </p>
      )}
      <a className="button button-primary" href="/api/v1/auth/login">
        Sign in
      </a>
    </main>
  );
}

function route(path, local) {
  let match;
  if (path === '/') return <Overview local={local} />;
  if (path === '/workspaces') return <Workspaces />;
  if ((match = path.match(/^\/workspaces\/(\d+)$/))) return <WorkspaceDetail key={match[1]} id={match[1]} />;
  if ((match = path.match(/^\/runs\/(\d+)$/))) return <RunDetail key={match[1]} id={match[1]} />;
  return (
    <>
      <h1>Page not found</h1>
      <p>
        <Link href="/">Go to the overview</Link>
      </p>
    </>
  );
}

export default function App() {
  const path = useLocationPath();
  const { data: me, error, loading, reload } = useApi('/me');

  useEffect(() => {
    if (me && window.location.search.includes('auth_error')) {
      navigate(window.location.pathname);
    }
  }, [me]);

  if (loading && !me) return <Loading />;
  if (error?.status === 401) return <SignIn />;
  if (error) {
    return (
      <main className="content">
        <ErrorMessage error={error} onRetry={reload} />
      </main>
    );
  }

  const signOut = async () => {
    await api('/auth/logout', { method: 'POST' }).catch(() => {});
    window.location.assign('/');
  };

  return (
    <>
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <header className="topbar">
        <Link href="/" className="brand">
          Bogform
        </Link>
        <nav aria-label="Main">
          <Link href="/" current={path === '/'}>
            Overview
          </Link>
          <Link href="/workspaces" current={path.startsWith('/workspaces')}>
            Workspaces
          </Link>
        </nav>
        {me.local ? (
          <div className="account">
            <span className="local-label" title="Read-only view of report files on this computer">
              Local
            </span>
          </div>
        ) : (
          <div className="account">
            <span className="account-name" title={me.email}>
              {me.name || me.email}
            </span>
            <button type="button" className="button" onClick={signOut}>
              Sign out
            </button>
          </div>
        )}
      </header>
      <main id="main" className="content">
        {route(path, Boolean(me.local))}
      </main>
    </>
  );
}
