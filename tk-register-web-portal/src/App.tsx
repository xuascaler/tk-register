import {
  BookOpen,
  Check,
  ChartNoAxesCombined,
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
import Members from './members/Members';
import { MemberScopeProvider } from './members/MemberScope';
import Config from './Config';
import { ErrorNotice, Loading } from './ui';
import SubmitAccount from './submissions/SubmitAccount';
import SubmissionHistory from './submissions/SubmissionHistory';
import SubmissionDetails from './submissions/SubmissionDetail';
import { AdminHistory, PoolAccountDetails } from './admin/AccountPool';
import APIToken from './account/APIToken';
import { AccountUsageDetails } from './accounts/Accounts';
import AccountsWorkspace from './accounts/AccountsWorkspace';
import { accountListHistoryState, clearAccountListReturns, cloneAccountListReturn, readAccountListReturn, type AccountListReturn } from './accounts/list-navigation';
import APIDocumentation, { type DocTopic, type DocSection } from './docs/APIDocumentation';

function Account({ user, status, tab, onTab, onViewAccounts }: { user: User; status: AuthStatus; tab: 'general' | 'members'; onTab: (tab: 'general' | 'members') => void; onViewAccounts: () => void }) {
  return (
    <>
      <div className='title-row'>
        <div>
          <h1>Account settings</h1>
          <p className='subtitle'>Your profile, sign-in details, and members.</p>
        </div>
      </div>
      <div className='member-settings-tabs' role='tablist' aria-label='Account settings'>
        {(['general', 'members'] as const).map(value => <button key={value} role='tab' id={`account-tab-${value}`} aria-selected={tab === value} aria-controls={`account-panel-${value}`} tabIndex={tab === value ? 0 : -1} onClick={() => onTab(value)} onKeyDown={event => { if (['ArrowLeft','ArrowRight','Home','End'].includes(event.key)) { event.preventDefault(); const next = event.key === 'Home' ? 'general' : event.key === 'End' ? 'members' : tab === 'general' ? 'members' : 'general'; onTab(next); requestAnimationFrame(() => document.getElementById(`account-tab-${next}`)?.focus()); } }}>{value === 'general' ? 'General' : 'Members'}</button>)}
      </div>
      <div role='tabpanel' id={`account-panel-${tab}`} aria-labelledby={`account-tab-${tab}`}>
      {tab === 'members' ? <Members currentEmail={user.email} onViewAccounts={onViewAccounts} /> : <>
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
      </>}
      </div>
    </>
  );
}

type Route = {
  page: 'submit' | 'accounts' | 'account-usage' | 'history' | 'account' | 'docs' | 'config' | 'detail' | 'pool-accounts' | 'pool-account' | 'pool-account-usage' | 'pool-history' | 'pool-submission';
  id?: string;
  config?: string;
  settingsTab?: 'general' | 'members';
  accountsTab?: 'overview' | 'list';
  backAccount?: string;
  backAccountUsage?: boolean;
  docTopic?: DocTopic;
  docSection?: DocSection;
};
function readRoute(): Route {
  const [path, query] = window.location.hash.replace(/^#\/?/, '').split('?');
  const segments = path.split('/');
  if (segments[0] === 'docs') {
    const docTopic = (['getting-started', 'submit-account', 'account-lookup', 'update-auth-json'] as const).find(topic => topic === segments[1]);
    const docSection = docTopic && docTopic !== 'getting-started'
      ? (['parameters', 'example', 'response', 'errors'] as const).find(section => section === segments[2])
      : undefined;
    return { page: 'docs', docTopic, docSection };
  }
  if (segments[0] === 'account-pool') {
    const back = new URLSearchParams(query);
    if (segments[1] === 'submissions') return segments[2]
      ? { page: 'pool-submission', id: segments[2], backAccount: back.get('synced_account') || back.get('account') || undefined, backAccountUsage: !!back.get('synced_account') }
      : { page: 'pool-history' };
    return segments[2] ? { page: segments[3] === 'usage' ? 'pool-account-usage' : 'pool-account', id: segments[2] } : { page: 'pool-accounts', accountsTab: back.get('tab') === 'list' ? 'list' : 'overview' };
  }
  if (segments[0] === 'accounts') return segments[1] ? { page: 'account-usage', id: segments[1] } : { page: 'accounts', accountsTab: new URLSearchParams(query).get('tab') === 'list' ? 'list' : 'overview' };
  if (segments[0] === 'submissions') return segments[1] ? { page: 'detail', id: segments[1], backAccount: new URLSearchParams(query).get('account') || undefined } : { page: 'history' };
  if (segments[0] === 'account') return { page: 'account', settingsTab: segments[1] === 'members' ? 'members' : 'general' };
  if (segments[0] === 'config') return {
    page: 'config', config: ['email', 'access', 'verification', 'sessions', 'vendors', 'account_sync', 'overview', 'users', 'bill_exports'].includes(segments[1]) ? segments[1] : 'email',
  };
  return { page: 'submit' };
}
function routeHash(route: Route) {
  if (route.page === 'account') return route.settingsTab === 'members' ? '#/account/members' : '#/account';
  if (route.page === 'docs') return `#/docs${route.docTopic ? `/${route.docTopic}` : ''}${route.docTopic && route.docTopic !== 'getting-started' && route.docSection && route.docSection !== 'parameters' ? `/${route.docSection}` : ''}`;
  if (route.page === 'pool-account-usage') return `#/account-pool/accounts/${encodeURIComponent(route.id || '')}/usage`;
  if (route.page === 'pool-accounts' || route.page === 'pool-account') return `#/account-pool/accounts${route.id ? `/${encodeURIComponent(route.id)}` : route.accountsTab === 'list' ? '?tab=list' : ''}`;
  if (route.page === 'pool-history' || route.page === 'pool-submission') return `#/account-pool/submissions${route.id ? `/${encodeURIComponent(route.id)}` : ''}${route.backAccount ? `?${route.backAccountUsage ? 'synced_account' : 'account'}=${encodeURIComponent(route.backAccount)}` : ''}`;
  if (route.page === 'accounts' || route.page === 'account-usage') return `#/accounts${route.id ? `/${encodeURIComponent(route.id)}` : route.accountsTab === 'list' ? '?tab=list' : ''}`;
  if (route.page === 'history' || route.page === 'detail') return `#/submissions${route.id ? `/${encodeURIComponent(route.id)}` : ''}${route.backAccount ? `?account=${encodeURIComponent(route.backAccount)}` : ''}`;
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
  const isAccountsPage = page === 'accounts' || page === 'account-usage' || (page === 'detail' && !!route.backAccount);
  const isHistoryPage = page === 'history' || (page === 'detail' && !route.backAccount);
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
      clearAccountListReturns();
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
  function navigate(next: Route, confirmed = false, restoredList?: AccountListReturn) {
    const hash = restoredList?.hash || routeHash(next);
    const sameList = !restoredList && route.page === next.page && route.accountsTab === 'list' && next.accountsTab === 'list';
    if (hash === window.location.hash || sameList) {
      setMenuOpen(false);
      return;
    }
    if (confirmed || canLeave()) {
      const changingDocSection = route.page === 'docs' && next.page === 'docs' && route.docTopic === next.docTopic;
      setRoute(next);
      const adminChain = next.page === 'pool-account-usage' || next.page === 'pool-account' || next.page === 'pool-submission' && !!next.backAccount;
      const memberChain = next.page === 'account-usage' || next.page === 'detail' && !!next.backAccount;
      const context = restoredList || (user && (adminChain || memberChain) ? readAccountListReturn(user.id, adminChain) : null);
      const leavingList = route.accountsTab === 'list' && (route.page === 'accounts' || route.page === 'pool-accounts');
      // Each new list visit and its detail chain keep separate scroll snapshots.
      // Scrolling a browser Back destination must not change an older detail's return position.
      const saved = context && (restoredList || leavingList) ? cloneAccountListReturn(context) : context;
      window.history.pushState(accountListHistoryState(saved), '', hash);
      setDirty(false);
      setError('');
      setMenuOpen(false);
      if (!changingDocSection) {
        window.scrollTo({ top: 0 });
        requestAnimationFrame(() =>
          document.getElementById('main-content')?.focus({ preventScroll: true }),
        );
      }
    }
  }
  function backToAccountList(admin: boolean) {
    const saved = user ? readAccountListReturn(user.id, admin) : null;
    navigate({ page: admin ? 'pool-accounts' : 'accounts', accountsTab: 'list' }, false, saved || undefined);
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
    const drawerViewport = window.matchMedia('(max-width: 820px)');
    if (!drawerViewport.matches) {
      setMenuOpen(false);
      return;
    }
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
    const handleViewportChange = () => {
      if (drawerViewport.matches) return;
      setMenuOpen(false);
      const focused = document.activeElement;
      if (focused instanceof HTMLElement && sidebar.current?.contains(focused) && focused.getClientRects().length === 0) {
        const navigation = sidebar.current;
        (navigation.querySelector<HTMLButtonElement>('.nav-item[aria-current="page"]') || navigation.querySelector<HTMLButtonElement>('.nav-item'))?.focus();
      }
    };
    window.addEventListener('keydown', handleKey);
    drawerViewport.addEventListener('change', handleViewportChange);
    return () => {
      document.body.style.overflow = oldOverflow;
      window.removeEventListener('keydown', handleKey);
      drawerViewport.removeEventListener('change', handleViewportChange);
    };
  }, [menuOpen]);
  async function logout() {
    if (signingOut || !canLeave()) return;
    setSigningOut(true);
    setError('');
    try {
      await request('/auth/logout', { method: 'POST', body: {} });
      clearAccountListReturns();
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
        <MemberScopeProvider key={user.id}><div className='workspace'>
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
              <button className={`nav-item ${isAccountsPage ? 'current' : ''}`} aria-current={isAccountsPage ? 'page' : undefined} onClick={() => navigate({ page: 'accounts' })}><ChartNoAxesCombined aria-hidden='true' />Accounts</button>
              <button
                className={`nav-item ${isHistoryPage ? 'current' : ''}`}
                aria-current={
                  isHistoryPage ? 'page' : undefined
                }
                onClick={() => navigate({ page: 'history' })}
              >
                <ClipboardList aria-hidden='true' />
                Submission history
              </button>
              <button
                className={`nav-item ${page === 'docs' ? 'current' : ''}`}
                aria-current={page === 'docs' ? 'page' : undefined}
                onClick={() => navigate({ page: 'docs' })}
              >
                <BookOpen aria-hidden='true' />
                API documentation
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
                    <button className={`nav-item ${page === 'pool-accounts' || page === 'pool-account' || page === 'pool-account-usage' || (page === 'pool-submission' && route.backAccount) ? 'current' : ''}`} aria-current={page === 'pool-accounts' || page === 'pool-account' || page === 'pool-account-usage' || (page === 'pool-submission' && route.backAccount) ? 'page' : undefined} onClick={() => navigate({ page: 'pool-accounts' })}>Accounts</button>
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
              <AccountsWorkspace key='admin-accounts' userID={user.id} admin tab={route.accountsTab || 'overview'} onTab={accountsTab => navigate({ page: 'pool-accounts', accountsTab })} onOpen={id => navigate({ page: 'pool-account-usage', id })} />
            ) : page === 'pool-account-usage' && route.id ? (
              <AccountUsageDetails key={`admin-${route.id}`} admin id={route.id} onBack={() => backToAccountList(true)} onSubmission={id => navigate({ page: 'pool-submission', id, backAccount: route.id, backAccountUsage: true })} />
            ) : page === 'pool-account' && route.id ? (
              <PoolAccountDetails key={route.id} id={route.id} onBack={() => backToAccountList(true)} onSubmission={id => navigate({ page: 'pool-submission', id, backAccount: route.id })} />
            ) : page === 'pool-history' ? (
              <AdminHistory onOpen={id => navigate({ page: 'pool-submission', id })} />
            ) : page === 'pool-submission' && route.id ? (
              <SubmissionDetails key={`admin-${route.id}`} id={route.id} endpointPrefix='/admin/submissions' backLabel={route.backAccount ? 'Account details' : 'All submission history'} onBack={() => navigate(route.backAccount ? { page: route.backAccountUsage ? 'pool-account-usage' : 'pool-account', id: route.backAccount } : { page: 'pool-history' })} />
            ) : page === 'config' ? (
              <Config
                key={route.config}
                initialCategory={route.config}
                currentUser={user}
                onDirty={setDirty}
              />
            ) : page === 'accounts' ? (
              <AccountsWorkspace userID={user.id} tab={route.accountsTab || 'overview'} onTab={accountsTab => navigate({ page: 'accounts', accountsTab })} onOpen={id => navigate({ page: 'account-usage', id })} />
            ) : page === 'account-usage' && route.id ? (
              <AccountUsageDetails key={`member-${route.id}`} id={route.id} onBack={() => backToAccountList(false)} onSubmission={id => navigate({ page: 'detail', id, backAccount: route.id })} />
            ) : page === 'account' ? (
              <Account user={user} status={status} tab={route.settingsTab || 'general'} onTab={settingsTab => navigate({ page:'account', settingsTab })} onViewAccounts={() => navigate({ page:'accounts', accountsTab: 'list' })} />
            ) : page === 'docs' ? (
              <APIDocumentation
                topic={route.docTopic}
                section={route.docSection}
                onNavigate={(docTopic, docSection) => navigate({ page: 'docs', docTopic, docSection })}
                onManageToken={() => {
                  navigate({ page: 'account' });
                  requestAnimationFrame(() => document.getElementById('api-token-title')?.scrollIntoView({ block: 'start' }));
                }}
              />
            ) : page === 'history' ? (
              <SubmissionHistory
                onOpen={(id) => navigate({ page: 'detail', id })}
                onSubmit={() => navigate({ page: 'submit' })}
              />
            ) : page === 'detail' && route.id ? (
              <SubmissionDetails
                key={route.id}
                id={route.id}
                backLabel={route.backAccount ? 'Account details' : 'Submission history'}
                onBack={() => navigate(route.backAccount ? { page: 'account-usage', id: route.backAccount } : { page: 'history' })}
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
        </div></MemberScopeProvider>
      )}
    </div>
  );
}
