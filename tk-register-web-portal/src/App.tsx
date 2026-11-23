import {
  BookOpen,
  Check,
  ChartNoAxesCombined,
  ClipboardList,
  ChevronDown,
  Globe,
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
  displayError, type DisplayError,
  request,
  setCSRFToken,
  type AuthStatus,
  type Session,
  type User,
} from './api';
import Auth from './Auth';
import { setLanguage, t, useLanguage } from './i18n';
import Select from './submissions/Select';
import Members from './members/Members';
import { MemberScopeProvider } from './members/MemberScope';
import Config from './Config';
import { ErrorNotice, Loading } from './ui';
import SubmitAccount from './submissions/SubmitAccount';
import SubmissionHistory from './submissions/SubmissionHistory';
import SubmissionDetails from './submissions/SubmissionDetail';
import { AdminHistory, PoolAccountDetails } from './admin/AccountPool';
import APIToken from './account/APIToken';
import ChangePassword from './account/ChangePassword';
import { AccountUsageDetails } from './accounts/Accounts';
import AccountsWorkspace from './accounts/AccountsWorkspace';
import { accountListHistoryState, clearAccountListReturns, cloneAccountListReturn, readAccountListReturn, type AccountListReturn } from './accounts/list-navigation';
import APIDocumentation, { type DocTopic, type DocSection } from './docs/APIDocumentation';

function Account({ user, status, tab, onTab, onViewAccounts, onPasswordChanged }: { user: User; status: AuthStatus; tab: 'general' | 'members'; onTab: (tab: 'general' | 'members') => void; onViewAccounts: () => void; onPasswordChanged: () => void }) {
  return (
    <>
      <div className='title-row'>
        <div>
          <h1>{t('Account settings')}</h1>
          <p className='subtitle'>{t('Your profile, sign-in details, and members.')}</p>
        </div>
      </div>
      <div className='member-settings-tabs' role='tablist' aria-label={t('Account settings')}>
        {(['general', 'members'] as const).map(value => <button key={value} role='tab' id={`account-tab-${value}`} aria-selected={tab === value} aria-controls={`account-panel-${value}`} tabIndex={tab === value ? 0 : -1} onClick={() => onTab(value)} onKeyDown={event => { if (['ArrowLeft','ArrowRight','Home','End'].includes(event.key)) { event.preventDefault(); const next = event.key === 'Home' ? 'general' : event.key === 'End' ? 'members' : tab === 'general' ? 'members' : 'general'; onTab(next); requestAnimationFrame(() => document.getElementById(`account-tab-${next}`)?.focus()); } }}>{value === 'general' ? t('General') : t('Members')}</button>)}
      </div>
      <div role='tabpanel' id={`account-panel-${tab}`} aria-labelledby={`account-tab-${tab}`}>
      {tab === 'members' ? <Members currentEmail={user.email} onViewAccounts={onViewAccounts} /> : <>
      <section className='account-section' aria-labelledby='profile-title'>
        <h2 id='profile-title'>{t('Profile')}</h2>
        <div className='account-row'>
          <div className='account-label'>{t('Email address')}</div>
          <div className='account-value'>
            <div className='value-line'>
              <span className='account-email'>{user.email}</span>
              <span className='verified'>
                <Check aria-hidden='true' />{t('Verified')}</span>
            </div>
            <p className='account-help'>{t('Your email address is your username.')}</p>
          </div>
        </div>
        <div className='account-row'>
          <div className='account-label'>{t('Account role')}</div>
          <div className='account-value'>
            {user.is_super_admin ? t('Super administrator') : user.role === 'admin' ? t('Administrator') : t('Member')}
          </div>
        </div>
      </section>
      <section className='account-section' aria-labelledby='signin-title'>
        <h2 id='signin-title'>{t('Sign-in')}</h2>
        <div className='account-row'>
          <div className='account-label'>{t('Authentication')}</div>
          <div className='account-value'>
            {user.role === 'admin'
              ? t('Password + email code')
              : status.password_login_enabled && status.email_code_login_enabled
                ? t('Password or email code')
                : status.email_code_login_enabled
                  ? t('Email code')
                  : t('Password')}
            <p className='account-help'>
              {user.role === 'admin'
                ? t('Email verification is required for every administrator sign-in.')
                : t('Sign in securely with your email address.')}
            </p>
          </div>
        </div>
        <ChangePassword email={user.email} onChanged={onPasswordChanged} />
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
  raw?: boolean;
  backAccount?: string;
  backAccountUsage?: boolean;
  docTopic?: DocTopic;
  docSection?: DocSection;
};
function readRoute(): Route {
  const [path, query] = window.location.hash.replace(/^#\/?/, '').split('?');
  const segments = path.split('/');
  if (segments[0] === 'docs') {
    const docTopic = (['getting-started', 'submit-account', 'account-lookup', 'daily-usage', 'update-auth-json'] as const).find(topic => topic === segments[1]);
    const docSection = docTopic && docTopic !== 'getting-started'
      ? (['parameters', 'example', 'response', 'errors'] as const).find(section => section === segments[2])
      : undefined;
    return { page: 'docs', docTopic, docSection };
  }
  if (segments[0] === 'account-pool') {
    const back = new URLSearchParams(query);
    if (segments[1] === 'submissions') return segments[2]
      ? { page: 'pool-submission', id: segments[2], backAccount: back.get('synced_account') || back.get('account') || undefined, backAccountUsage: !!back.get('synced_account'), raw: back.get('from') === 'raw' }
      : { page: 'pool-history' };
    const raw = segments[1] === 'raw-accounts';
    return segments[2] ? { raw, page: raw || segments[3] === 'usage' ? 'pool-account-usage' : 'pool-account', id: segments[2] } : { page: 'pool-accounts', raw, accountsTab: back.get('tab') === 'list' ? 'list' : 'overview' };
  }
  if (segments[0] === 'accounts') return segments[1] ? { page: 'account-usage', id: segments[1] } : { page: 'accounts', accountsTab: new URLSearchParams(query).get('tab') === 'list' ? 'list' : 'overview' };
  if (segments[0] === 'submissions') return segments[1] ? { page: 'detail', id: segments[1], backAccount: new URLSearchParams(query).get('account') || undefined } : { page: 'history' };
  if (segments[0] === 'account') return { page: 'account', settingsTab: segments[1] === 'members' ? 'members' : 'general' };
  if (segments[0] === 'config') return {
    page: 'config', config: ['email', 'access', 'verification', 'sessions', 'vendors', 'account_sync', 'daily_usage', 'overview', 'users', 'bill_exports'].includes(segments[1]) ? segments[1] : 'email',
  };
  return { page: 'submit' };
}
function routeHash(route: Route) {
  if (route.page === 'account') return route.settingsTab === 'members' ? '#/account/members' : '#/account';
  if (route.page === 'docs') return `#/docs${route.docTopic ? `/${route.docTopic}` : ''}${route.docTopic && route.docTopic !== 'getting-started' && route.docSection && route.docSection !== 'parameters' ? `/${route.docSection}` : ''}`;
  const poolAccountsPath = route.raw ? '#/account-pool/raw-accounts' : '#/account-pool/accounts';
  if (route.page === 'pool-account-usage') return `${poolAccountsPath}/${encodeURIComponent(route.id || '')}/usage`;
  if (route.page === 'pool-accounts' || route.page === 'pool-account') return `${poolAccountsPath}${route.id ? `/${encodeURIComponent(route.id)}` : route.accountsTab === 'list' ? '?tab=list' : ''}`;
  if (route.page === 'pool-history' || route.page === 'pool-submission') return `#/account-pool/submissions${route.id ? `/${encodeURIComponent(route.id)}` : ''}${route.backAccount ? `?${route.backAccountUsage ? 'synced_account' : 'account'}=${encodeURIComponent(route.backAccount)}${route.raw ? '&from=raw' : ''}` : ''}`;
  if (route.page === 'accounts' || route.page === 'account-usage') return `#/accounts${route.id ? `/${encodeURIComponent(route.id)}` : route.accountsTab === 'list' ? '?tab=list' : ''}`;
  if (route.page === 'history' || route.page === 'detail') return `#/submissions${route.id ? `/${encodeURIComponent(route.id)}` : ''}${route.backAccount ? `?account=${encodeURIComponent(route.backAccount)}` : ''}`;
  if (route.page === 'config') return `#/config${route.config && route.config !== 'email' ? `/${route.config}` : ''}`;
  return `#/${route.page}`;
}

export default function App() {
  const language = useLanguage();
  const [status, setStatus] = useState<AuthStatus | null>(null);
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<DisplayError>('');
  const [message, setMessage] = useState('');
  const [route, setRoute] = useState<Route>(readRoute);
  const page = route.page;
  const isPoolPage = page.startsWith('pool-');
  const isAccountsPage = page === 'accounts' || page === 'account-usage' || (page === 'detail' && !!route.backAccount);
  const isHistoryPage = page === 'history' || (page === 'detail' && !route.backAccount);
  const poolAccountsActive = page === 'pool-accounts' || page === 'pool-account' || page === 'pool-account-usage' || (page === 'pool-submission' && !!route.backAccount);
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
        if (!controller.signal.aborted) setError(displayError(cause));
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
      t('Leave this page? Unsaved changes and account credentials will be discarded. If a submission is pending, check its history before submitting again.'),
    );
  function navigate(next: Route, confirmed = false, restoredList?: AccountListReturn) {
    const hash = restoredList?.hash || routeHash(next);
    const sameList = !restoredList && !!route.raw === !!next.raw && route.page === next.page && route.accountsTab === 'list' && next.accountsTab === 'list';
    if (hash === window.location.hash || sameList) {
      setMenuOpen(false);
      return;
    }
    if (confirmed || canLeave()) {
      const changingDocSection = route.page === 'docs' && next.page === 'docs' && route.docTopic === next.docTopic;
      setRoute(next);
      const adminChain = next.page === 'pool-account-usage' || next.page === 'pool-account' || next.page === 'pool-submission' && !!next.backAccount;
      const memberChain = next.page === 'account-usage' || next.page === 'detail' && !!next.backAccount;
      const context = restoredList || (user && (adminChain || memberChain) ? readAccountListReturn(user.id, adminChain, next.raw) : null);
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
    const saved = user ? readAccountListReturn(user.id, admin, route.raw) : null;
    navigate({ page: admin ? 'pool-accounts' : 'accounts', raw: route.raw, accountsTab: 'list' }, false, saved || undefined);
  }
  useEffect(() => {
    const followHistory = () => {
      if (
        dirty &&
        !window.confirm(t('Discard your unsaved changes and leave this page?'))
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
      setError(displayError(cause));
    } finally {
      setSigningOut(false);
    }
  }
  function passwordChanged() {
    clearAccountListReturns();
    setCSRFToken('');
    setUser(null);
    setDirty(false);
    setError('');
    setMenuOpen(false);
    setRoute({ page: 'submit' });
    window.history.replaceState(null, '', '#/submit');
    setMessage('Password updated. Please sign in again.');
    requestAnimationFrame(() => document.getElementById('auth-title')?.focus());
  }
  return (
    <div id='tk-app'>
      <a href='#main-content' className='skip-link'>{t('Skip to content')}</a>
      <header className='header'>
        {user && (
          <button
            ref={menuButton}
            className='menu-button'
            aria-label={t('Open navigation')}
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
          aria-label={t('TK Register home')}
          onClick={(event) => {
            if (!canLeave()) event.preventDefault();
          }}
        >
          <span className='brand-word'>tk-register</span>
        </a>
        <div className='language-picker'>
          <Globe aria-hidden='true' />
          <Select id='site-language' label={t('Language')} hiddenLabel value={language}
            options={[{ value: 'en-US', label: 'English' }, { value: 'zh-CN', label: '简体中文' }]}
            onChange={value => setLanguage(value === 'zh-CN' ? 'zh-CN' : 'en-US')} />
        </div>
        {user && (
          <button
            className='identity'
            aria-label={t('Open account for {{email}}', { email: user.email })}
            title={user.email}
            onClick={() => navigate({ page: 'account' })}
          >
            <span className='avatar'>{user.email[0]?.toUpperCase()}</span>
          </button>
        )}
      </header>
      {loading ? (
        <main id='main-content' className='status-page'>
          <Loading label={t('Loading your account…')} />
        </main>
      ) : !status ? (
        <main id='main-content' className='status-page'>
          <h1>{t('Unable to load TK Register')}</h1>
          <ErrorNotice>{error}</ErrorNotice>
          <button
            className='button primary'
            onClick={() => setAttempt((value) => value + 1)}
          >{t('Try again')}</button>
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
            aria-label={t('Workspace navigation')}
            role={menuOpen ? 'dialog' : undefined}
            aria-modal={menuOpen ? true : undefined}
          >
            <div className='drawer-heading'>
              <strong>TK Register</strong>
              <button
                className='menu-button'
                aria-label={t('Close navigation')}
                onClick={() => {
                  setMenuOpen(false);
                  menuButton.current?.focus();
                }}
              >
                <X aria-hidden='true' />
              </button>
            </div>
            <nav className='nav-group' aria-label={t('My account')}>
              <p className='eyebrow'>{t('My account')}</p>
              <button
                className={`nav-item ${page === 'submit' ? 'current' : ''}`}
                aria-current={page === 'submit' ? 'page' : undefined}
                onClick={() => navigate({ page: 'submit' })}
              >
                <Plus aria-hidden='true' />{t('Submit account')}</button>
              <button className={`nav-item ${isAccountsPage ? 'current' : ''}`} aria-current={isAccountsPage ? 'page' : undefined} onClick={() => navigate({ page: 'accounts' })}><ChartNoAxesCombined aria-hidden='true' />{language === 'zh-CN' ? t('Account list') : t('Accounts')}</button>
              <button
                className={`nav-item ${isHistoryPage ? 'current' : ''}`}
                aria-current={
                  isHistoryPage ? 'page' : undefined
                }
                onClick={() => navigate({ page: 'history' })}
              >
                <ClipboardList aria-hidden='true' />{t('Submission history')}</button>
              <button
                className={`nav-item ${page === 'docs' ? 'current' : ''}`}
                aria-current={page === 'docs' ? 'page' : undefined}
                onClick={() => navigate({ page: 'docs' })}
              >
                <BookOpen aria-hidden='true' />{t('API documentation')}</button>
              <button
                className={`nav-item ${page === 'account' ? 'current' : ''}`}
                aria-current={page === 'account' ? 'page' : undefined}
                onClick={() => navigate({ page: 'account' })}
              >
                <UserRound aria-hidden='true' />{t('Account settings')}</button>
            </nav>
            {user.role === 'admin' && (
              <nav className='nav-group' aria-label={t('Management')}>
                <p className='eyebrow'>{t('Management')}</p>
                <div className='admin-nav-branch'>
                  <button className={`nav-item admin-pool-toggle ${isPoolPage ? 'active-branch' : ''}`} aria-expanded={poolOpen} aria-controls='account-pool-nav' onClick={() => setPoolOpen(value => !value)}>
                    <Layers aria-hidden='true' />{t('Account pool')}<ChevronDown className={poolOpen ? 'is-expanded' : ''} aria-hidden='true' />
                  </button>
                  {poolOpen && <div className='admin-subnav' id='account-pool-nav'>
                    <button className={`nav-item ${poolAccountsActive && !route.raw ? 'current' : ''}`} aria-current={poolAccountsActive && !route.raw ? 'page' : undefined} onClick={() => navigate({ page: 'pool-accounts' })}>{language === 'zh-CN' ? t('Account list') : t('Accounts')}</button>
                    {user.is_super_admin && <button className={`nav-item ${poolAccountsActive && route.raw ? 'current' : ''}`} aria-current={poolAccountsActive && route.raw ? 'page' : undefined} onClick={() => navigate({ page: 'pool-accounts', raw: true })}>{t('Raw account')}</button>}
                    <button className={`nav-item ${page === 'pool-history' || (page === 'pool-submission' && !route.backAccount) ? 'current' : ''}`} aria-current={page === 'pool-history' || (page === 'pool-submission' && !route.backAccount) ? 'page' : undefined} onClick={() => navigate({ page: 'pool-history' })}>{t('Submission history')}</button>
                  </div>}
                </div>
                <button
                  className={`nav-item ${page === 'config' ? 'current' : ''}`}
                  aria-current={page === 'config' ? 'page' : undefined}
                  onClick={() => navigate({ page: 'config' })}
                >
                  <Settings2 aria-hidden='true' />{t('Config')}</button>
              </nav>
            )}
            <div className='signout'>
              <button
                className='nav-item'
                disabled={signingOut}
                onClick={logout}
              >
                <LogOut aria-hidden='true' />
                <span>{signingOut ? t('Signing out…') : t('Sign out')}</span>
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
              <><h1>{t('Admin access required')}</h1><p className='subtitle'>{t('This page is available to administrators.')}</p><button className='button secondary' onClick={() => navigate({ page: 'history' })}>{t('Your submission history')}</button></>
            ) : route.raw && !user.is_super_admin ? (
              <><h1>{t('Super admin access required')}</h1><button className='button secondary' onClick={() => navigate({ page: 'pool-accounts' })}>{t('Back to Accounts')}</button></>
            ) : page === 'pool-accounts' ? (
              <AccountsWorkspace key={route.raw ? 'raw-accounts' : 'admin-accounts'} raw={route.raw} userID={user.id} admin tab={route.accountsTab || 'overview'} onTab={accountsTab => navigate({ page: 'pool-accounts', raw: route.raw, accountsTab })} onOpen={id => navigate({ page: 'pool-account-usage', raw: route.raw, id })} />
            ) : page === 'pool-account-usage' && route.id ? (
              <AccountUsageDetails key={`${route.raw ? 'raw' : 'admin'}-${route.id}`} raw={route.raw} admin id={route.id} onBack={() => backToAccountList(true)} onSubmission={id => navigate({ page: 'pool-submission', id, backAccount: route.id, backAccountUsage: true, raw: route.raw })} />
            ) : page === 'pool-account' && route.id ? (
              <PoolAccountDetails key={route.id} id={route.id} onBack={() => backToAccountList(true)} onSubmission={id => navigate({ page: 'pool-submission', id, backAccount: route.id })} />
            ) : page === 'pool-history' ? (
              <AdminHistory onOpen={id => navigate({ page: 'pool-submission', id })} />
            ) : page === 'pool-submission' && route.id ? (
              <SubmissionDetails key={`admin-${route.id}`} id={route.id} endpointPrefix='/admin/submissions' backLabel={route.backAccount ? t('Account details') : t('All submission history')} onBack={() => navigate(route.backAccount ? { page: route.backAccountUsage ? 'pool-account-usage' : 'pool-account', id: route.backAccount, raw: route.raw } : { page: 'pool-history' })} />
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
              <Account user={user} status={status} tab={route.settingsTab || 'general'} onTab={settingsTab => navigate({ page:'account', settingsTab })} onViewAccounts={() => navigate({ page:'accounts', accountsTab: 'list' })} onPasswordChanged={passwordChanged} />
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
                backLabel={route.backAccount ? t('Account details') : t('Submission history')}
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
