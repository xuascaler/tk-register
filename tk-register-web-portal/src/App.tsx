import {
  Check,
  ClipboardList,
  ChevronDown,
  Layers,
  LogOut,
  Menu,
  Plus,
  Settings2,
  UserRound,
  X,
} from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import {
  APIError,
  errorMessage,
  request,
  setCSRFToken,
  type AuthStatus,
  type Session,
  type User,
} from './api';
import Auth from './Auth';
import Config from './Config';
import { ErrorNotice, Loading } from './ui';
import SubmitAccount from './submissions/SubmitAccount';
import SubmissionHistory from './submissions/SubmissionHistory';
import SubmissionDetails from './submissions/SubmissionDetail';
import { Accounts, AdminHistory, PoolAccountDetails } from './admin/AccountPool';
import APIToken from './account/APIToken';

function Account({ user, status }: { user: User; status: AuthStatus }) {
  return (
    <>
      <div className='title-row'>
        <div>
          <h1>Account settings</h1>
          <p className='subtitle'>Your identity and sign-in details.</p>
        </div>
      </div>
      <section className='account-section' aria-labelledby='profile-title'>
        <h2 id='profile-title'>Profile</h2>
        <div className='account-row'>
          <div className='account-label'>Email address</div>
          <div className='account-value'>
            <div className='value-line'>
              <span className='account-email'>{user.email}</span>
              <span className='verified'>
                <Check aria-hidden='true' />
                Verified
              </span>
            </div>
            <p className='account-help'>Your email address is your username.</p>
          </div>
        </div>
        <div className='account-row'>
          <div className='account-label'>Account role</div>
          <div className='account-value'>
            {user.role === 'admin' ? 'Administrator' : 'Member'}
          </div>
        </div>
      </section>
      <section className='account-section' aria-labelledby='signin-title'>
        <h2 id='signin-title'>Sign-in</h2>
        <div className='account-row'>
          <div className='account-label'>Authentication</div>
          <div className='account-value'>
            {user.role === 'admin'
              ? 'Password + email code'
              : status.password_login_enabled && status.email_code_login_enabled
                ? 'Password or email code'
                : status.email_code_login_enabled
                  ? 'Email code'
                  : 'Password'}
            <p className='account-help'>
              {user.role === 'admin'
                ? 'Email verification is required for every administrator sign-in.'
                : 'Sign in securely with your email address.'}
            </p>
          </div>
        </div>
      </section>
      <APIToken key={user.id} />
    </>
  );
}

type Route = {
  page: 'submit' | 'history' | 'account' | 'config' | 'detail' | 'pool-accounts' | 'pool-account' | 'pool-history' | 'pool-submission';
  id?: string;
  config?: string;
  backAccount?: string;
};
function readRoute(): Route {
  const [path, query] = window.location.hash.replace(/^#\/?/, '').split('?');
  const segments = path.split('/');
  if (segments[0] === 'account-pool') {
    if (segments[1] === 'submissions') return segments[2]
      ? { page: 'pool-submission', id: segments[2], backAccount: new URLSearchParams(query).get('account') || undefined }
      : { page: 'pool-history' };
    return segments[2] ? { page: 'pool-account', id: segments[2] } : { page: 'pool-accounts' };
  }
  if (segments[0] === 'submissions') return segments[1] ? { page: 'detail', id: segments[1] } : { page: 'history' };
  if (segments[0] === 'account') return { page: 'account' };
  if (segments[0] === 'config') return {
    page: 'config', config: ['email', 'access', 'verification', 'sessions', 'vendors', 'users'].includes(segments[1]) ? segments[1] : 'email',
  };
  return { page: 'submit' };
}
function routeHash(route: Route) {
  if (route.page === 'pool-accounts' || route.page === 'pool-account') return `#/account-pool/accounts${route.id ? `/${encodeURIComponent(route.id)}` : ''}`;
  if (route.page === 'pool-history' || route.page === 'pool-submission') return `#/account-pool/submissions${route.id ? `/${encodeURIComponent(route.id)}` : ''}${route.backAccount ? `?account=${encodeURIComponent(route.backAccount)}` : ''}`;
  if (route.page === 'history' || route.page === 'detail') return `#/submissions${route.id ? `/${encodeURIComponent(route.id)}` : ''}`;
  if (route.page === 'config') return `#/config${route.config && route.config !== 'email' ? `/${route.config}` : ''}`;
  return `#/${route.page}`;
}

export default function App() {
  const [status, setStatus] = useState<AuthStatus | null>(null);
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');
  const [route, setRoute] = useState<Route>(readRoute);
  const page = route.page;
  const isPoolPage = page.startsWith('pool-');
  const [poolOpen, setPoolOpen] = useState(isPoolPage);
  useEffect(() => { if (isPoolPage) setPoolOpen(true); }, [isPoolPage]);
  const [menuOpen, setMenuOpen] = useState(false);
  const menuButton = useRef<HTMLButtonElement>(null);
  const sidebar = useRef<HTMLElement>(null);
  const [dirty, setDirty] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const acceptSession = useCallback((session: Session) => {
    setCSRFToken(session.csrf_token);
    setUser(session.user);
    setMessage('');
    setError('');
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError('');
    Promise.all([
      request<AuthStatus>('/auth/status', { signal: controller.signal }),
      request<Session>('/auth/me', { signal: controller.signal }).catch(
        (cause) => {
          if (cause instanceof APIError && cause.status === 401) return null;
          throw cause;
        },
      ),
    ])
      .then(([publicStatus, session]) => {
        setStatus(publicStatus);
        if (session?.user) acceptSession(session);
        else {
          setUser(null);
          setCSRFToken('');
        }
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [attempt, acceptSession]);
  useEffect(() => {
    const expired = () => {
      setUser(null);
      setDirty(false);
      setCSRFToken('');
      setRoute({ page: 'submit' });
      setMenuOpen(false);
      window.history.replaceState(null, '', '#/submit');
      setMessage('Your session has expired. Please sign in again.');
    };
    window.addEventListener('tk-session-expired', expired);
    return () => window.removeEventListener('tk-session-expired', expired);
  }, []);
  const canLeave = () =>
    !dirty ||
    window.confirm(
      'Leave this page? Unsaved changes and account credentials will be discarded. If a submission is pending, check its history before submitting again.',
    );
  function navigate(next: Route, confirmed = false) {
    if (routeHash(next) === routeHash(route)) {
      setMenuOpen(false);
      return;
    }
    if (confirmed || canLeave()) {
      setRoute(next);
      window.history.pushState(null, '', routeHash(next));
      setDirty(false);
      setError('');
      setMenuOpen(false);
      window.scrollTo({ top: 0 });
      requestAnimationFrame(() =>
        document.getElementById('main-content')?.focus(),
      );
    }
  }
  useEffect(() => {
    const followHistory = () => {
      if (
        dirty &&
        !window.confirm('Discard your unsaved changes and leave this page?')
      ) {
        window.history.replaceState(null, '', routeHash(route));
        return;
      }
      setRoute(readRoute());
      setDirty(false);
      setMenuOpen(false);
    };
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (dirty) event.preventDefault();
    };
    window.addEventListener('hashchange', followHistory);
    window.addEventListener('beforeunload', beforeUnload);
    return () => {
      window.removeEventListener('hashchange', followHistory);
      window.removeEventListener('beforeunload', beforeUnload);
    };
  }, [dirty, route]);
  useEffect(() => {
    if (!menuOpen) return;
    const oldOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    sidebar.current?.querySelector<HTMLButtonElement>('button')?.focus();
    const handleKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setMenuOpen(false);
        menuButton.current?.focus();
      }
      if (event.key === 'Tab') {
        const buttons = [...(sidebar.current?.querySelectorAll<HTMLButtonElement>(
          'button:not(:disabled)',
        ) || [])].filter(button => button.getClientRects().length > 0);
        if (!buttons?.length) return;
        const first = buttons[0],
          last = buttons[buttons.length - 1];
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
      }
    };
    window.addEventListener('keydown', handleKey);
    return () => {
      document.body.style.overflow = oldOverflow;
      window.removeEventListener('keydown', handleKey);
    };
  }, [menuOpen]);
  async function logout() {
    if (signingOut || !canLeave()) return;
    setSigningOut(true);
    setError('');
    try {
      await request('/auth/logout', { method: 'POST', body: {} });
      setUser(null);
      setDirty(false);
      setCSRFToken('');
      setRoute({ page: 'submit' });
      window.history.replaceState(null, '', '#/submit');
      setMenuOpen(false);
      setMessage('');
      setAttempt((value) => value + 1);
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setSigningOut(false);
    }
  }
  return (
    <div id='tk-app'>
      <a href='#main-content' className='skip-link'>
        Skip to content
      </a>
      <header className='header'>
        {user && (
          <button
            ref={menuButton}
            className='menu-button'
            aria-label='Open navigation'
            aria-expanded={menuOpen}
            aria-controls='workspace-navigation'
            onClick={() => setMenuOpen(true)}
          >
            <Menu aria-hidden='true' />
          </button>
        )}
        <a
          href='/tk-register'
          className='brand'
          aria-label='TK Register home'
          onClick={(event) => {
            if (!canLeave()) event.preventDefault();
          }}
        >
          <span className='brand-word'>tk-register</span>
        </a>
        {user && (
          <button
            className='identity'
            aria-label={`Open account for ${user.email}`}
            title={user.email}
            onClick={() => navigate({ page: 'account' })}
          >
            <span className='avatar'>{user.email[0]?.toUpperCase()}</span>
          </button>
        )}
      </header>
      {loading ? (
        <main id='main-content' className='status-page'>
          <Loading label='Loading your account…' />
        </main>
      ) : !status ? (
        <main id='main-content' className='status-page'>
          <h1>Unable to load TK Register</h1>
          <ErrorNotice>{error}</ErrorNotice>
          <button
            className='button primary'
            onClick={() => setAttempt((value) => value + 1)}
          >
            Try again
          </button>
        </main>
      ) : !user ? (
        <div id='main-content'>
          <Auth status={status} onSession={acceptSession} message={message} />
        </div>
      ) : (
        <div className='workspace'>
          {menuOpen && (
            <div
              className='sidebar-backdrop'
              onClick={() => setMenuOpen(false)}
            />
          )}
          <aside
            ref={sidebar}
            id='workspace-navigation'
            className={`sidebar ${menuOpen ? 'is-open' : ''}`}
            aria-label='Workspace navigation'
            role={menuOpen ? 'dialog' : undefined}
            aria-modal={menuOpen ? true : undefined}
          >
            <div className='drawer-heading'>
              <strong>TK Register</strong>
              <button
                className='menu-button'
                aria-label='Close navigation'
                onClick={() => {
                  setMenuOpen(false);
                  menuButton.current?.focus();
                }}
              >
                <X aria-hidden='true' />
              </button>
            </div>
            <nav className='nav-group' aria-label='My account'>
              <p className='eyebrow'>My account</p>
              <button
                className={`nav-item ${page === 'submit' ? 'current' : ''}`}
                aria-current={page === 'submit' ? 'page' : undefined}
                onClick={() => navigate({ page: 'submit' })}
              >
                <Plus aria-hidden='true' />
                Submit account
              </button>
              <button
                className={`nav-item ${page === 'history' || page === 'detail' ? 'current' : ''}`}
                aria-current={
                  page === 'history' || page === 'detail' ? 'page' : undefined
                }
                onClick={() => navigate({ page: 'history' })}
              >
                <ClipboardList aria-hidden='true' />
                Submission history
              </button>
              <button
                className={`nav-item ${page === 'account' ? 'current' : ''}`}
                aria-current={page === 'account' ? 'page' : undefined}
                onClick={() => navigate({ page: 'account' })}
              >
                <UserRound aria-hidden='true' />
                Account settings
              </button>
            </nav>
            {user.role === 'admin' && (
              <nav className='nav-group' aria-label='Management'>
                <p className='eyebrow'>Management</p>
                <div className='admin-nav-branch'>
                  <button className={`nav-item admin-pool-toggle ${isPoolPage ? 'active-branch' : ''}`} aria-expanded={poolOpen} aria-controls='account-pool-nav' onClick={() => setPoolOpen(value => !value)}>
                    <Layers aria-hidden='true' />Account pool<ChevronDown className={poolOpen ? 'is-expanded' : ''} aria-hidden='true' />
                  </button>
                  {poolOpen && <div className='admin-subnav' id='account-pool-nav'>
                    <button className={`nav-item ${page === 'pool-accounts' || page === 'pool-account' || (page === 'pool-submission' && route.backAccount) ? 'current' : ''}`} aria-current={page === 'pool-accounts' || page === 'pool-account' || (page === 'pool-submission' && route.backAccount) ? 'page' : undefined} onClick={() => navigate({ page: 'pool-accounts' })}>Accounts</button>
                    <button className={`nav-item ${page === 'pool-history' || (page === 'pool-submission' && !route.backAccount) ? 'current' : ''}`} aria-current={page === 'pool-history' || (page === 'pool-submission' && !route.backAccount) ? 'page' : undefined} onClick={() => navigate({ page: 'pool-history' })}>Submission history</button>
                  </div>}
                </div>
                <button
                  className={`nav-item ${page === 'config' ? 'current' : ''}`}
                  aria-current={page === 'config' ? 'page' : undefined}
                  onClick={() => navigate({ page: 'config' })}
                >
                  <Settings2 aria-hidden='true' />
                  Config
                </button>
              </nav>
            )}
            <div className='signout'>
              <button
                className='nav-item'
                disabled={signingOut}
                onClick={logout}
              >
                <LogOut aria-hidden='true' />
                <span>{signingOut ? 'Signing out…' : 'Sign out'}</span>
              </button>
            </div>
          </aside>
          <main
            id='main-content'
            className='main'
            tabIndex={-1}
            inert={menuOpen ? true : undefined}
          >
            <ErrorNotice>{error}</ErrorNotice>
            {(page === 'config' || isPoolPage) && user.role !== 'admin' ? (
              <><h1>Admin access required</h1><p className='subtitle'>This page is available to administrators.</p><button className='button secondary' onClick={() => navigate({ page: 'history' })}>Your submission history</button></>
            ) : page === 'pool-accounts' ? (
              <Accounts onOpen={id => navigate({ page: 'pool-account', id })} />
            ) : page === 'pool-account' && route.id ? (
              <PoolAccountDetails key={route.id} id={route.id} onBack={() => navigate({ page: 'pool-accounts' })} onSubmission={id => navigate({ page: 'pool-submission', id, backAccount: route.id })} />
            ) : page === 'pool-history' ? (
              <AdminHistory onOpen={id => navigate({ page: 'pool-submission', id })} />
            ) : page === 'pool-submission' && route.id ? (
              <SubmissionDetails key={`admin-${route.id}`} id={route.id} endpointPrefix='/admin/submissions' backLabel={route.backAccount ? 'Account details' : 'All submission history'} onBack={() => navigate(route.backAccount ? { page: 'pool-account', id: route.backAccount } : { page: 'pool-history' })} />
            ) : page === 'config' ? (
              <Config
                key={route.config}
                initialCategory={route.config}
                currentUser={user}
                onDirty={setDirty}
              />
            ) : page === 'account' ? (
              <Account user={user} status={status} />
            ) : page === 'history' ? (
              <SubmissionHistory
                onOpen={(id) => navigate({ page: 'detail', id })}
                onSubmit={() => navigate({ page: 'submit' })}
              />
            ) : page === 'detail' && route.id ? (
              <SubmissionDetails
                key={route.id}
                id={route.id}
                onBack={() => navigate({ page: 'history' })}
              />
            ) : (
              <SubmitAccount
                user={user}
                onDirty={setDirty}
                onCreated={(id) => navigate({ page: 'detail', id }, true)}
                onHistory={() => navigate({ page: 'history' })}
                onConfig={() => navigate({ page: 'config', config: 'vendors' })}
              />
            )}
          </main>
        </div>
      )}
    </div>
  );
}
