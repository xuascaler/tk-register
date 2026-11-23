import { t, currentLanguage, useLanguage, translateMessage } from '../i18n';
import { ArrowLeft, ArrowUpRight, Clock3, RefreshCw, Search } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { APIError, displayError, type DisplayError, errorMessage, request, retryAfterSeconds, retryStage, type RetryStage } from '../api';
import { ErrorNotice, Loading } from '../ui';
import SourceFilter from '../members/SourceFilter';
import { useMemberScope } from '../members/MemberScope';
import Select from '../submissions/Select';
import { AdminBadge } from '../admin/shared';
import { formatCost, formatPercent, formatTime, formatTokens, knownNumber, statusLabel, statusTone, supplierStatusDetail, type Metric } from './helpers';
import type { AccountDetails, AccountPage, SyncedAccount } from './types';
import { ErrorPopover } from './ErrorPopover';
import { UsageTrend } from './UsageTrend';
import { DailyUsage } from './DailyUsage';
import { AuthenticationCard, AuthJSONEditor } from './AuthJSONEditor';
import { authJSONResultMessage, type AuthJSONUpdateResult } from './auth-json';
import { loadAccountPage, mergeSyncedAccount, syncAccountRow, syncRetryLabel, type AccountRowSyncResult } from './row-sync';
import { deletionInProgress } from './account-state';
import { useAccountListNavigation } from './list-navigation';
import { AccountPagination } from './AccountPagination';

function Status({ account }: { account: SyncedAccount }) {
  const snapshot = account.snapshot;
  const label = statusLabel(snapshot?.account_status);
  const className = `usage-status ${statusTone(snapshot?.account_status)}`;
  if (!snapshot || !snapshot.account_status || snapshot.account_status === 'active') return <span className={className}>{label}</span>;
  return <ErrorPopover label={label} title={t('Supplier status · {{label}}', { label })} className={className}
    message={snapshot.upstream_error_message?.trim() || (snapshot.upstream_error_message == null ? t('No supplier error details have been collected yet.') : t('The supplier did not provide an additional reason.'))}
    detail={snapshot.upstream_status_raw ? supplierStatusDetail(snapshot.upstream_status_raw) : undefined}
    note={snapshot.upstream_error_message?.trim() ? t('Last reported supplier reason. Manual Sync checks for an updated reason when available.') : t('This is the supplier account status, separate from whether TK Register collected the data successfully.')} />;
}

function UsageWindow({ value, label }: { value: number | null | undefined; label: string }) {
  return <div className='usage-window' aria-label={`${label}: ${formatPercent(value)}`}>
    <span className='usage-window-label'>{label}</span><span className='usage-number'>{formatPercent(value)}</span>
    {knownNumber(value) && <span className='usage-meter' aria-hidden='true'><span style={{ width: `${Math.max(0, Math.min(100, value))}%` }} /></span>}
  </div>;
}

function SubmittedTime({ value }: { value: string | null | undefined }) {
  if (!value || !Number.isFinite(Date.parse(value))) return <span className='usage-date'>—</span>;
  const date = new Date(value);
  return <time className='usage-submitted-time' dateTime={value} title={formatTime(value)}>
    {new Intl.DateTimeFormat(currentLanguage(), { month: 'short', day: 'numeric', year: 'numeric' }).format(date)}
    <span>{new Intl.DateTimeFormat(currentLanguage(), { hour: '2-digit', minute: '2-digit' }).format(date)}</span>
  </time>;
}

export function MemberAccounts({ userID, onOpen, admin = false, embedded = false, raw = false }: { userID: number; onOpen: (id: string) => void; admin?: boolean; embedded?: boolean; raw?: boolean }) {
  const memberScope = useMemberScope();
  const navigation = useAccountListNavigation(userID, admin, memberScope.selection, raw);
  const { page, search: query, status, sources } = navigation.filters;
  useLanguage();
  const actionEndpoint = admin ? '/admin/synced-accounts' : '/accounts';
  const endpoint = raw ? '/admin/raw-accounts' : actionEndpoint;
  const [data, setData] = useState<AccountPage | null>(null);
  const [search, setSearch] = useState(query);
  const [attempt, setAttempt] = useState(0);
  const [loading, setLoading] = useState(true);
  const [syncingID, setSyncingID] = useState<string | null>(null);
  const [rowResults, setRowResults] = useState<Record<string, AccountRowSyncResult & { retryAt?: number }>>({});
  const [now, setNow] = useState(Date.now);
  const [loadedScope, setLoadedScope] = useState('');
  const [error, setError] = useState<DisplayError>('');
  const pending = useRef<{ id: string; controller: AbortController } | null>(null);
  const revision = useRef(0);
  const scope = `${endpoint}:${page}:${query}:${status}:${sources}:${attempt}`;
  useEffect(() => setSearch(query), [query]);
  useEffect(() => { if (!admin && memberScope.selection !== sources) memberScope.setSelection(sources); }, [admin, sources, memberScope.selection, memberScope.setSelection]);
  useEffect(() => {
    const nextQuery = search.trim();
    if (nextQuery === query) return;
    const timer = window.setTimeout(() => navigation.change({ search: nextQuery, page: 1 }, { replace: true }), 300);
    return () => window.clearTimeout(timer);
  }, [search, query, navigation.change]);
  useEffect(() => {
    const controller = new AbortController();
    const version = ++revision.current;
    setLoading(true); setSyncingID(null); setError(''); setRowResults({});
    const params = new URLSearchParams({ page: String(page), search: query, status, sources });
    void loadAccountPage(endpoint, params, controller.signal)
      .then(result => { if (!controller.signal.aborted && version === revision.current) {
        const lastPage = Math.max(1, Math.ceil(result.total / result.page_size));
        if (page > lastPage) { navigation.change({ page: lastPage }, { replace: true, keepScroll: true }); return; }
        setData(result); setLoadedScope(scope);
      } })
      .catch(cause => { if (!controller.signal.aborted && version === revision.current) setError(displayError(cause)); })
      .finally(() => { if (!controller.signal.aborted && version === revision.current) setLoading(false); });
    return () => {
      controller.abort();
      pending.current?.controller.abort();
      pending.current = null;
    };
  }, [page, query, status, attempt, endpoint, scope]);
  useEffect(() => {
    if (data && !loading && loadedScope === scope) navigation.restoreScroll();
  }, [data, loading, loadedScope, scope, navigation.restoreScroll]);
  useEffect(() => {
    const deadlines = Object.values(rowResults).map(result => result.retryAt || 0);
    if (!deadlines.some(deadline => deadline > Date.now())) return;
    setNow(Date.now());
    const timer = window.setInterval(() => {
      const time = Date.now(); setNow(time);
      if (!deadlines.some(deadline => deadline > time)) window.clearInterval(timer);
    }, 1000);
    return () => window.clearInterval(timer);
  }, [rowResults]);
  async function sync(account: SyncedAccount) {
    // The ref prevents double clicks before React has rendered the disabled
    // buttons. One explicit request at a time avoids competing supplier leases.
    if (pending.current || loading || loadedScope !== scope || deletionInProgress(account) || !account.current_submission_id || (rowResults[account.id]?.retryAt || 0) > Date.now()) return;
    const operation = { id: account.id, controller: new AbortController() };
    const version = revision.current;
    pending.current = operation; setSyncingID(account.id);
    setRowResults(current => { const next = { ...current }; delete next[account.id]; return next; });
    try {
      const result = await syncAccountRow(actionEndpoint, account, operation.controller.signal);
      if (operation.controller.signal.aborted || pending.current !== operation || version !== revision.current) return;
      setData(current => current && mergeSyncedAccount(current, result));
      setRowResults(current => ({ ...current, [account.id]: { ...result, retryAt: result.retryAfter ? Date.now() + result.retryAfter * 1000 : undefined } }));
    } catch (cause) {
      if (!operation.controller.signal.aborted && pending.current === operation && version === revision.current) {
        setRowResults(current => ({ ...current, [account.id]: { account, status: 'failed', message: errorMessage(cause) } }));
      }
    } finally {
      if (pending.current === operation) { pending.current = null; setSyncingID(null); }
    }
  }
  return <section className='usage-accounts'>
    {!embedded && <div className='title-row'><div>{admin && <p className='eyebrow admin-breadcrumb'>{t('Account pool')}</p>}<h1>{t('Accounts')}</h1><p className='subtitle'>{t('Saved account status, usage, and cumulative spend.')}</p></div>{admin && <AdminBadge />}</div>}
    <div className={`usage-toolbar ${admin ? '' : 'member-accounts-toolbar'}`}>
      <label className='usage-search'><Search aria-hidden='true' /><span className='sr-only'>{t('Search accounts')}</span><input value={search} onChange={event => setSearch(event.target.value)} type='search' placeholder={admin ? t('Search account or user email') : t('Search by account email')} maxLength={254} /></label>
      {!admin && <SourceFilter id='account-source' onChange={sources => navigation.change({ sources, page: 1 })} />}
      <Select id='usage-account-status' label={t('Account status')} hiddenLabel={admin} value={status} onChange={value => navigation.change({ status: value, page: 1 })} options={[{ value: '', label: t('All account statuses') }, { value: 'active', label: t('Active') }, { value: 'offline', label: t('Offline') }, { value: 'banned', label: t('Banned') }, { value: 'sync_failed', label: t('Sync error') }, { value: 'login_failed', label: t('Login failed') }, { value: 're_login_failed', label: t('Re-login failed') }, { value: 'bound_failed', label: t('Binding failed') }, { value: 'cooking_failed', label: t('Setup failed') }, { value: 'pending', label: t('Pending') }, { value: 'unknown', label: t('Unknown') }]} />
    </div>
    <ErrorNotice>{error}</ErrorNotice>
    {error && <button className='button pale usage-load-retry' disabled={loading} onClick={() => setAttempt(value => value + 1)}>{t('Try again')}</button>}
    {!data ? (loading ? <Loading label={t('Loading accounts…')} /> : null) : <div className={`usage-list-shell ${loading ? 'usage-list-loading' : ''}`} aria-busy={loading}>
      <div className='usage-table-scroll'><table className={`usage-accounts-table ${admin ? 'usage-admin-table' : 'member-accounts-table'}`}>
        <caption className='sr-only'>{admin ? t('Accounts across all users and their latest successfully collected data') : t('Your accounts and their latest successfully collected data')}</caption>
        <thead><tr><th scope='col'>{t('Account / plan')}</th><th scope='col'>{admin ? t('User') : t('Source')}</th><th scope='col'>{t('Status')}</th><th scope='col'>{t('Usage windows')}</th><th scope='col' className='usage-total-cell'>{t('Total spent')}</th><th scope='col' title={t('Most recent submission time')}>{t('Submitted')}</th><th scope='col'>{t('Last updated')}</th><th scope='col' className='usage-sync-heading'>{t('Sync')}</th></tr></thead>
        <tbody>{data?.items.map(account => {
          const refresh = rowResults[account.id];
          const busy = syncingID === account.id;
          const deleting = deletionInProgress(account);
          const eligible = Boolean(account.current_submission_id);
          const failure = refresh?.status === 'failed' ? refresh.message : refresh?.status === 'updated' ? '' : account.last_error;
          const stale = Boolean(failure || refresh?.status === 'skipped');
          const retryIn = Math.max(0, Math.ceil(((refresh?.retryAt || 0) - now) / 1000));
          const rateLimited = !!refresh?.retryAfter || /rate.limit|limiting requests|too many requests|http 429/i.test(failure || '');
          return <tr key={account.id} aria-busy={busy || deleting}>
          <td className='usage-account-cell'><button className='usage-email' onClick={() => { navigation.remember(account.id); onOpen(account.id); }}>{account.account}<ArrowUpRight aria-hidden='true' /></button><span className='usage-secondary'>{account.plan}</span><span className='usage-small'>{t('Account ID')} {account.platform_account_id || account.remote_id || '—'}</span></td>
          <td data-label={admin ? t('User') : t('Source')} className='usage-owner-cell'><span className='usage-owner-email'>{admin ? account.user_email || '—' : account.account_source?.label && account.account_source.id !== 'myself' ? account.account_source.label : t('myself')}</span></td>
          <td data-label={t('Status')}><Status account={account} />{stale && account.snapshot && <span className='usage-small'>{t('Last known')}</span>}</td>
          <td data-label={t('Usage windows')}><UsageWindow label={t('5 hours')} value={account.snapshot?.usage_5h_percent} /><UsageWindow label={t('7 days')} value={account.snapshot?.usage_7d_percent} /></td>
          <td data-label={t('Total spent')} className='usage-total-cell'><strong className='usage-money'>{formatCost(account.snapshot?.cost_total_usd, 'USD')}</strong>{stale && account.snapshot && <span className='usage-small'>{t('Last known total')}</span>}</td>
          <td data-label={t('Submitted')}><SubmittedTime value={account.latest_submitted_at} /></td>
          <td data-label={t('Last updated')}><span className='usage-date'>{formatTime(account.last_success_at, undefined, true)}</span>
            <div className='usage-row-feedback' aria-live='polite'>{busy ? <span className='usage-row-sync'>{t('Updating account…')}</span> : !eligible ? <span className='usage-small'>{t('Awaiting acceptance')}</span> : failure ? <ErrorPopover label={rateLimited ? t('Rate limited') : t('Sync failed')} title={t('Account synchronization failed')} className='usage-sync-error' message={failure} note={account.snapshot ? t('Showing the last successful data.') : t('No data has been collected yet.')} detail={account.last_attempt_at ? t('Last saved attempt: {{time}}', { time: formatTime(account.last_attempt_at) }) : undefined} /> : refresh?.status === 'skipped' ? <ErrorPopover label={t('Not updated')} title={t('Account was not updated')} className='usage-sync-error' message={refresh.message || t('Another sync is in progress. Try again shortly.')} /> : refresh?.status === 'updated' ? refresh.message ? <ErrorPopover label={t('Details unavailable')} title={t('Account updated')} className='usage-sync-error' message={refresh.message} /> : <span className='usage-row-success'>{t('Updated')}</span> : !account.snapshot ? <span className='usage-small'>{t('No data collected')}</span> : null}</div>
          </td>
          <td className='usage-sync-cell'><button className='button pale usage-sync-button' type='button' aria-label={t('Sync {{account}}', { account: account.account })} disabled={loading || loadedScope !== scope || syncingID !== null || deleting || !eligible || retryIn > 0} onClick={() => void sync(account)}><RefreshCw className={busy ? 'usage-spinning' : ''} aria-hidden='true' />{busy ? t('Syncing') : t('Sync')}</button>{retryIn > 0 && !deleting && <span className='usage-sync-cooldown'>{syncRetryLabel(retryIn, refresh?.retryStage)}</span>}</td>
        </tr>; })}</tbody>
      </table></div>
      {!data?.items.length && <div className='usage-empty'><h2>{query || status ? t('No matching accounts') : t('No accounts yet')}</h2><p>{query || status ? t('Try another email or account status.') : admin ? t('Submitted accounts from all users will appear here with their latest available data.') : t('Your submitted accounts will appear here with their latest available data.')}</p></div>}
      {data && data.total > 0 && <AccountPagination page={data.page} total={data.total} pageSize={data.page_size} loading={loading || loadedScope !== scope} scope={loadedScope} onPage={page => navigation.change({ page })} />}
    </div>}
    <p className='usage-footnote'><Clock3 aria-hidden='true' />{t('Use Sync to update an individual account. Automatic collection follows the configured schedule. Failed updates retain the last successful data.')}</p>
  </section>;
}

const metricNames: Record<Metric, string> = { cost_total_usd: 'Total spent', input_tokens_total: 'Input tokens', output_tokens_total: 'Output tokens' };

export function AccountUsageDetails({ id, onBack, onSubmission, admin = false, raw = false }: { id: string; onBack: () => void; onSubmission: (id: string) => void; admin?: boolean; raw?: boolean }) {
  useLanguage();
  const actionEndpoint = admin ? '/admin/synced-accounts' : '/accounts';
  const endpoint = raw ? '/admin/raw-accounts' : actionEndpoint;
  const [data, setData] = useState<AccountDetails | null>(null);
  const [tab, setTab] = useState<'overview' | 'daily'>(() => new URLSearchParams(window.location.hash.split('?')[1]).get('tab') === 'daily-usage' ? 'daily' : 'overview');
  function changeTab(next: 'overview' | 'daily') { setTab(next); const path = window.location.hash.split('?')[0]; window.history.replaceState(window.history.state, '', `${path}${next === 'daily' ? '?tab=daily-usage' : ''}`); }
  const [days, setDays] = useState('7');
  const [metric, setMetric] = useState<Metric>('cost_total_usd');
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [editorOpen, setEditorOpen] = useState(false);
  const [refreshWarning, setRefreshWarning] = useState('');
  const [syncRetry, setSyncRetry] = useState<{ until: number; stage?: RetryStage } | null>(null);
  const [syncNow, setSyncNow] = useState(Date.now);
  const syncRetryIn = Math.max(0, Math.ceil(((syncRetry?.until || 0) - syncNow) / 1000));
  const [error, setError] = useState<DisplayError>('');
  const [notice, setNotice] = useState('');
  const [attempt, setAttempt] = useState(0);
  const pending = useRef<AbortController | null>(null);
  useEffect(() => {
    setEditorOpen(false); setRefreshWarning(''); setNotice(''); setSyncing(false); setSyncRetry(null);
    return () => pending.current?.abort();
  }, [id, endpoint]);
  useEffect(() => {
    const controller = new AbortController(); setLoading(true); setError('');
    request<AccountDetails>(`${endpoint}/${encodeURIComponent(id)}?days=${days}`, { signal: controller.signal })
      .then(result => { if (!controller.signal.aborted) setData(result); })
      .catch(cause => { if (!controller.signal.aborted) setError(displayError(cause)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [id, days, attempt, endpoint]);
  useEffect(() => {
    if (!syncRetry || syncRetry.until <= Date.now()) return;
    setSyncNow(Date.now());
    const timer = window.setInterval(() => {
      const now = Date.now(); setSyncNow(now);
      if (now >= syncRetry.until) window.clearInterval(timer);
    }, 1000);
    return () => window.clearInterval(timer);
  }, [syncRetry]);
  async function sync() {
    if (syncing || data?.account && deletionInProgress(data.account) || (syncRetry?.until || 0) > Date.now()) return;
    const controller = new AbortController(); pending.current = controller;
    setSyncing(true); setError(''); setNotice(''); setRefreshWarning(''); setSyncRetry(null);
    try {
      if (!data?.account) return;
      const result = await syncAccountRow(actionEndpoint, data.account, controller.signal);
      if (!controller.signal.aborted) {
        if (result.status !== 'updated') {
          setError(result.message || 'The account could not be updated.');
          if (result.retryAfter) setSyncRetry({ until: Date.now() + result.retryAfter * 1000, stage: result.retryStage });
        } else if (result.message) setRefreshWarning(result.message);
        else setNotice('Account data updated.');
      }
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(displayError(cause));
        if (cause instanceof APIError && retryAfterSeconds(cause.retryAfter)) {
          setSyncRetry({ until: Date.now() + cause.retryAfter * 1000, stage: cause.retryStage });
        }
      }
    }
    finally {
      if (!controller.signal.aborted) {
        try { const result = await request<AccountDetails>(`${endpoint}/${encodeURIComponent(id)}?days=${days}`, { signal: controller.signal }); if (!controller.signal.aborted) setData(result); } catch (cause) { if (!controller.signal.aborted) setError(displayError(cause)); }
        if (!controller.signal.aborted) setSyncing(false);
      }
    }
  }
  async function credentialsSaved(result: AuthJSONUpdateResult) {
    setEditorOpen(false); setError(''); setSyncRetry(null);
    // The returned account may represent a newer lifetime. Trust its timestamp
    // instead of attaching the completed operation's timestamp to that identity.
    setData(current => current && { ...current, account: result.account || current.account });
    if (result.refresh_status === 'succeeded' || result.refresh_status === 'pending') {
      setNotice(authJSONResultMessage(result)); setRefreshWarning('');
    } else {
      setNotice(''); setRefreshWarning(authJSONResultMessage(result));
      const retryAfter = retryAfterSeconds(result.refresh_retry_after);
      if (retryAfter) setSyncRetry({ until: Date.now() + retryAfter * 1000, stage: retryStage(result.refresh_retry_stage) });
    }
    // The mutation is already confirmed. A separate read failure must not be
    // presented as a failed credential update or discard the returned account.
    const controller = new AbortController(); pending.current = controller; setLoading(true);
    try {
      const next = await request<AccountDetails>(`${endpoint}/${encodeURIComponent(id)}?days=${days}`, { signal: controller.signal });
      if (!controller.signal.aborted) setData(next);
    } catch {
      if (!controller.signal.aborted) setRefreshWarning(current => `${current ? `${current} ` : 'Auth JSON updated. '}The latest page data could not be loaded. Refresh this page to try again.`);
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  }
  const account = data?.account, snapshot = account?.snapshot;
  return <section className='usage-detail'>
    <button className='usage-back' onClick={onBack}><ArrowLeft aria-hidden='true' />{raw ? t('Account pool / Raw account') : admin ? t('Account pool / Accounts') : t('Accounts')}</button>
    {!account || !data ? <><ErrorNotice>{error}</ErrorNotice>{loading ? <Loading label={t('Loading account…')} /> : <button className='button pale' onClick={() => setAttempt(v => v + 1)}>{t('Try again')}</button>}</> : <>
      <div className='title-row usage-detail-title'><div><h1>{account.account}</h1><p className='subtitle'>{account.plan} <span aria-hidden='true'>·</span> {account.vendor_name}</p></div><div className='usage-detail-actions'><Status account={account} /><button className='button pale' disabled={syncing || loading || deletionInProgress(account) || syncRetryIn > 0 || !account.current_submission_id} onClick={sync}><RefreshCw className={syncing ? 'usage-spinning' : ''} aria-hidden='true' />{syncing ? t('Updating…') : t('Sync now')}</button></div></div>
      <div className='usage-meta'>{!admin && account.account_source && <span>{t('Source')} <strong>{account.account_source.id === 'myself' ? t('myself') : account.account_source.label}</strong></span>}{admin && <span className='usage-owner-meta'>{t('User')} <strong>{account.user_email || '—'}</strong></span>}<span>{t('Account ID')} <strong>{account.platform_account_id || account.remote_id || '—'}</strong></span><span>{t('Last updated')} <strong>{formatTime(account.last_success_at, data.timezone)}</strong></span>{account.latest_submission_id && <button className='usage-text-button' onClick={() => onSubmission(account.current_submission_id || account.latest_submission_id)}>{t('View submission')}<ArrowUpRight aria-hidden='true' /></button>}</div>
      <ErrorNotice>{error}</ErrorNotice>
      {deletionInProgress(account) && <p className='notice' role='status'>{t('Deletion is processing. Submission and usage history will be retained.')}</p>}
      {notice && <p className='notice success' role='status'>{translateMessage(notice)}</p>}
      {refreshWarning && <p className='notice account-auth-refresh-warning' role='status'>{translateMessage(refreshWarning)}</p>}
      {syncRetryIn > 0 && <p className='usage-small' role='status'>{syncRetryLabel(syncRetryIn, syncRetry?.stage)}. {t('Saved account data is retained.')}</p>}
      {account.last_error && <div className='usage-stale-notice' role='status'><strong>{t('Latest update failed')}</strong><p>{snapshot ? t('Showing the last successful data from {{time}}.', {time: formatTime(account.last_success_at, data.timezone) }) : t('No data has been collected for this account yet.')}</p><ErrorPopover label={t('Sync failed')} title={t('Account synchronization failed')} className='usage-sync-error' message={account.last_error} detail={t('Last saved attempt: {{time}}', { time: formatTime(account.last_attempt_at, data.timezone) })} /></div>}
      <div className='member-settings-tabs account-usage-tabs' role='tablist' aria-label={t('Account information')}>{(['overview', 'daily'] as const).map(value => <button key={value} role='tab' id={`usage-tab-${value}`} aria-selected={tab === value} aria-controls={`usage-panel-${value}`} tabIndex={tab === value ? 0 : -1} onClick={() => changeTab(value)} onKeyDown={e => { if (['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) { e.preventDefault(); const next = e.key === 'Home' ? 'overview' : e.key === 'End' ? 'daily' : tab === 'daily' ? 'overview' : 'daily'; changeTab(next); requestAnimationFrame(() => document.getElementById(`usage-tab-${next}`)?.focus()); } }}>{value === 'overview' ? t('Overview') : t('Daily usage')}</button>)}</div>
      <div role='tabpanel' id={`usage-panel-${tab}`} aria-labelledby={`usage-tab-${tab}`}>
      {tab === 'overview' && <>
      {(admin || account.can_manage !== false) && <AuthenticationCard account={account} disabled={syncing || loading} timezone={data.timezone} onEdit={() => setEditorOpen(true)} />}
      <div className='usage-metrics'>
        <div><span>{t('Total spent')}</span><strong>{formatCost(snapshot?.cost_total_usd, 'USD')}</strong><small>{account.last_error && snapshot ? t('Last known cumulative total') : t('Cumulative total')}</small></div>
        <div><span>{t('Input tokens')}</span><strong>{formatTokens(snapshot?.input_tokens_total, true)}</strong><small>{formatTokens(snapshot?.input_tokens_total)} {t('tokens')}</small></div>
        <div><span>{t('Output tokens')}</span><strong>{formatTokens(snapshot?.output_tokens_total, true)}</strong><small>{formatTokens(snapshot?.output_tokens_total)} {t('tokens')}</small></div>
      </div>
      <div className='usage-windows-panel'><div><UsageWindow label={t('5-hour usage')} value={snapshot?.usage_5h_percent} /><span className='usage-small'>{t('Resets')} {formatTime(snapshot?.reset_5h_at, data.timezone)}</span></div><div><UsageWindow label={t('7-day usage')} value={snapshot?.usage_7d_percent} /><span className='usage-small'>{t('Resets')} {formatTime(snapshot?.reset_7d_at, data.timezone)}</span></div></div>
      <section className='usage-trend-panel' aria-labelledby='usage-trend-title'><div className='usage-trend-heading'><div><h2 id='usage-trend-title'>{t('Cumulative usage')}</h2><p className='usage-small'>{t('Saved observations, collected every {{seconds}} seconds when enabled.', { seconds: data.interval_seconds.toLocaleString(currentLanguage()) })}</p></div><Select id='usage-date-range' label={t('Date range')} hiddenLabel value={days} onChange={setDays} disabled={syncing || loading} options={[{ value: '7', label: t('Last 7 days') }, { value: '30', label: t('Last 30 days') }, { value: '90', label: t('Last 90 days') }]} /></div>
        <div className='usage-metric-tabs' role='group' aria-label={t('Trend metric')}>{(Object.keys(metricNames) as Metric[]).map(key => <button key={key} aria-pressed={metric === key} onClick={() => setMetric(key)}>{t(metricNames[key])}</button>)}</div>
        {loading ? <Loading label={t('Loading usage history…')} /> : <UsageTrend key={`${id}:${days}`} data={data} endpoint={`${endpoint}/${encodeURIComponent(id)}/snapshots`} days={days} metric={metric} />}
      </section>
      </>}
      <DailyUsage key={`${endpoint}:${id}`} endpoint={`${endpoint}/${encodeURIComponent(id)}`} compact={tab === 'overview'} onOpen={() => changeTab('daily')} />
      </div>
      {editorOpen && <AuthJSONEditor account={account} endpoint={actionEndpoint} onClose={() => setEditorOpen(false)} onSaved={credentialsSaved} />}
    </>}
  </section>;
}
