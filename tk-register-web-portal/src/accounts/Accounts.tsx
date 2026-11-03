import { ArrowLeft, ArrowUpRight, ChevronLeft, ChevronRight, Clock3, RefreshCw, Search } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { APIError, errorMessage, request, retryAfterSeconds, retryStage, type RetryStage } from '../api';
import { ErrorNotice, Loading } from '../ui';
import Select from '../submissions/Select';
import { AdminBadge } from '../admin/shared';
import { formatCost, formatPercent, formatTime, formatTokens, knownNumber, statusLabel, statusTone, type Metric } from './helpers';
import type { AccountDetails, AccountPage, SyncedAccount } from './types';
import { UsageTrend } from './UsageTrend';
import { AuthenticationCard, AuthJSONEditor } from './AuthJSONEditor';
import { authJSONResultMessage, type AuthJSONUpdateResult } from './auth-json';
import { loadAccountPage, mergeSyncedAccount, syncAccountRow, syncRetryLabel, type AccountRowSyncResult } from './row-sync';

function Status({ account }: { account: SyncedAccount }) {
  return <span className={`usage-status ${statusTone(account.snapshot?.account_status)}`}>{statusLabel(account.snapshot?.account_status)}</span>;
}

function UsageWindow({ value, label }: { value: number | null | undefined; label: string }) {
  return <div className='usage-window' aria-label={`${label}: ${formatPercent(value)}`}>
    <span className='usage-window-label'>{label}</span><span className='usage-number'>{formatPercent(value)}</span>
    {knownNumber(value) && <span className='usage-meter' aria-hidden='true'><span style={{ width: `${Math.max(0, Math.min(100, value))}%` }} /></span>}
  </div>;
}

export function MemberAccounts({ onOpen, admin = false }: { onOpen: (id: string) => void; admin?: boolean }) {
  const endpoint = admin ? '/admin/synced-accounts' : '/accounts';
  const [data, setData] = useState<AccountPage | null>(null);
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState('');
  const [page, setPage] = useState(1);
  const [attempt, setAttempt] = useState(0);
  const [loading, setLoading] = useState(true);
  const [syncingID, setSyncingID] = useState<string | null>(null);
  const [rowResults, setRowResults] = useState<Record<string, AccountRowSyncResult & { retryAt?: number }>>({});
  const [now, setNow] = useState(Date.now);
  const [loadedScope, setLoadedScope] = useState('');
  const [error, setError] = useState('');
  const pending = useRef<{ id: string; controller: AbortController } | null>(null);
  const revision = useRef(0);
  const scope = `${endpoint}:${page}:${query}:${status}:${attempt}`;
  useEffect(() => {
    const nextQuery = search.trim();
    if (nextQuery === query) return;
    const timer = window.setTimeout(() => { setQuery(nextQuery); setPage(1); }, 300);
    return () => window.clearTimeout(timer);
  }, [search, query]);
  useEffect(() => {
    const controller = new AbortController();
    const version = ++revision.current;
    setLoading(true); setSyncingID(null); setError(''); setRowResults({});
    const params = new URLSearchParams({ page: String(page), search: query, status });
    void loadAccountPage(endpoint, params, controller.signal)
      .then(result => { if (!controller.signal.aborted && version === revision.current) { setData(result); setLoadedScope(scope); } })
      .catch(cause => { if (!controller.signal.aborted && version === revision.current) setError(errorMessage(cause)); })
      .finally(() => { if (!controller.signal.aborted && version === revision.current) setLoading(false); });
    return () => {
      controller.abort();
      pending.current?.controller.abort();
      pending.current = null;
    };
  }, [page, query, status, attempt, endpoint, scope]);
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
    if (pending.current || loading || loadedScope !== scope || !account.current_submission_id || (rowResults[account.id]?.retryAt || 0) > Date.now()) return;
    const operation = { id: account.id, controller: new AbortController() };
    const version = revision.current;
    pending.current = operation; setSyncingID(account.id);
    setRowResults(current => { const next = { ...current }; delete next[account.id]; return next; });
    try {
      const result = await syncAccountRow(endpoint, account, operation.controller.signal);
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
    <div className='title-row'><div>{admin && <p className='eyebrow admin-breadcrumb'>Account pool</p>}<h1>Accounts</h1><p className='subtitle'>Saved account status, usage, and cumulative spend.</p></div>{admin && <AdminBadge />}</div>
    <div className='usage-toolbar'>
      <label className='usage-search'><Search aria-hidden='true' /><span className='sr-only'>Search accounts</span><input value={search} onChange={event => setSearch(event.target.value)} type='search' placeholder={admin ? 'Search account or user email' : 'Search by account email'} maxLength={254} /></label>
      <Select id='usage-account-status' label='Account status' hiddenLabel value={status} onChange={value => { setStatus(value); setPage(1); }} options={[{ value: '', label: 'All account statuses' }, { value: 'active', label: 'Active' }, { value: 'offline', label: 'Offline' }, { value: 'banned', label: 'Banned' }, { value: 'unknown', label: 'Unknown' }]} />
    </div>
    <ErrorNotice>{error}</ErrorNotice>
    {error && <button className='button pale usage-load-retry' disabled={loading} onClick={() => setAttempt(value => value + 1)}>Try again</button>}
    {!data ? (loading ? <Loading label='Loading accounts…' /> : null) : <div className={`usage-list-shell ${loading ? 'usage-list-loading' : ''}`} aria-busy={loading}>
      <div className='usage-table-scroll'><table className={`usage-accounts-table ${admin ? 'usage-admin-table' : ''}`}>
        <caption className='sr-only'>{admin ? 'Accounts across all users and their latest successfully collected data' : 'Your accounts and their latest successfully collected data'}</caption>
        <thead><tr><th scope='col'>Account / plan</th>{admin && <th scope='col'>User</th>}<th scope='col'>Status</th><th scope='col'>Usage windows</th><th scope='col' className='usage-total-cell'>Total spent</th><th scope='col'>Last updated</th><th scope='col' className='usage-sync-heading'>Sync</th></tr></thead>
        <tbody>{data?.items.map(account => {
          const refresh = rowResults[account.id];
          const busy = syncingID === account.id;
          const eligible = Boolean(account.current_submission_id);
          const failure = refresh?.status === 'failed' ? refresh.message : refresh?.status === 'updated' ? '' : account.last_error;
          const stale = Boolean(failure || refresh?.status === 'skipped');
          const retryIn = Math.max(0, Math.ceil(((refresh?.retryAt || 0) - now) / 1000));
          const rateLimited = /rate.limit|limiting requests|too many requests|http 429/i.test(failure || '');
          return <tr key={account.id} aria-busy={busy}>
          <td className='usage-account-cell'><button className='usage-email' onClick={() => onOpen(account.id)}>{account.account}<ArrowUpRight aria-hidden='true' /></button><span className='usage-secondary'>{account.plan}</span><span className='usage-small'>Account ID {account.platform_account_id || account.remote_id || '—'}</span></td>
          {admin && <td data-label='User' className='usage-owner-cell'><span className='usage-owner-email'>{account.user_email || '—'}</span></td>}
          <td data-label='Status'><Status account={account} />{stale && account.snapshot && <span className='usage-small'>Last known</span>}</td>
          <td data-label='Usage windows'><UsageWindow label='5 hours' value={account.snapshot?.usage_5h_percent} /><UsageWindow label='7 days' value={account.snapshot?.usage_7d_percent} /></td>
          <td data-label='Total spent' className='usage-total-cell'><strong className='usage-money'>{formatCost(account.snapshot?.cost_total_usd, 'USD')}</strong>{stale && account.snapshot && <span className='usage-small'>Last known total</span>}</td>
          <td data-label='Last updated'><span className='usage-date'>{formatTime(account.last_success_at, undefined, true)}</span>
            <div className='usage-row-feedback' aria-live='polite'>{busy ? <span className='usage-row-sync'>Updating account…</span> : !eligible ? <span className='usage-small'>Awaiting acceptance</span> : failure ? <details className='usage-sync-error'><summary>{rateLimited ? 'Rate limited' : 'Sync failed'}</summary><p>{failure}</p><span>{account.snapshot ? 'Showing the last successful data.' : 'No data has been collected yet.'}</span></details> : refresh?.status === 'skipped' ? <details className='usage-sync-error'><summary>Not updated</summary><p>{refresh.message}</p></details> : refresh?.status === 'updated' ? <span className='usage-row-success'>Updated</span> : !account.snapshot ? <span className='usage-small'>No data collected</span> : null}</div>
          </td>
          <td className='usage-sync-cell'><button className='button pale usage-sync-button' type='button' aria-label={`Sync ${account.account}`} disabled={loading || loadedScope !== scope || syncingID !== null || !eligible || retryIn > 0} onClick={() => void sync(account)}><RefreshCw className={busy ? 'usage-spinning' : ''} aria-hidden='true' />{busy ? 'Syncing' : 'Sync'}</button>{retryIn > 0 && <span className='usage-sync-cooldown'>{syncRetryLabel(retryIn, refresh?.retryStage)}</span>}</td>
        </tr>; })}</tbody>
      </table></div>
      {!data?.items.length && <div className='usage-empty'><h2>{query || status ? 'No matching accounts' : 'No accounts yet'}</h2><p>{query || status ? 'Try another email or account status.' : admin ? 'Submitted accounts from all users will appear here with their latest available data.' : 'Your submitted accounts will appear here with their latest available data.'}</p></div>}
      {data && data.total > 0 && <div className='usage-pagination'><span>{data.total.toLocaleString()} {data.total === 1 ? 'account' : 'accounts'} · Page {data.page} of {Math.max(1, Math.ceil(data.total / data.page_size))}</span><div><button className='button pale' disabled={loading || page <= 1} onClick={() => setPage(p => p - 1)}><ChevronLeft aria-hidden='true' />Previous</button><button className='button pale' disabled={loading || page * data.page_size >= data.total} onClick={() => setPage(p => p + 1)}>Next<ChevronRight aria-hidden='true' /></button></div></div>}
    </div>}
    <p className='usage-footnote'><Clock3 aria-hidden='true' />Use Sync to update an individual account. Automatic collection follows the configured schedule. Failed updates retain the last successful data.</p>
  </section>;
}

const metricNames: Record<Metric, string> = { cost_total_usd: 'Total spent', input_tokens_total: 'Input tokens', output_tokens_total: 'Output tokens' };

export function AccountUsageDetails({ id, onBack, onSubmission, admin = false }: { id: string; onBack: () => void; onSubmission: (id: string) => void; admin?: boolean }) {
  const endpoint = admin ? '/admin/synced-accounts' : '/accounts';
  const [data, setData] = useState<AccountDetails | null>(null);
  const [days, setDays] = useState('7');
  const [metric, setMetric] = useState<Metric>('cost_total_usd');
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [editorOpen, setEditorOpen] = useState(false);
  const [refreshWarning, setRefreshWarning] = useState('');
  const [syncRetry, setSyncRetry] = useState<{ until: number; stage?: RetryStage } | null>(null);
  const [syncNow, setSyncNow] = useState(Date.now);
  const syncRetryIn = Math.max(0, Math.ceil(((syncRetry?.until || 0) - syncNow) / 1000));
  const [error, setError] = useState('');
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
      .catch(cause => { if (!controller.signal.aborted) setError(errorMessage(cause)); })
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
    if (syncing || (syncRetry?.until || 0) > Date.now()) return;
    const controller = new AbortController(); pending.current = controller;
    setSyncing(true); setError(''); setNotice(''); setRefreshWarning(''); setSyncRetry(null);
    try {
      const result = await request<{ account: SyncedAccount }>(`${endpoint}/${encodeURIComponent(id)}/sync`, { method: 'POST', body: {}, signal: controller.signal });
      if (!controller.signal.aborted) {
        if (result.account.last_error) setError(result.account.last_error);
        else setNotice('Account data updated.');
      }
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(errorMessage(cause));
        if (cause instanceof APIError && retryAfterSeconds(cause.retryAfter)) {
          setSyncRetry({ until: Date.now() + cause.retryAfter * 1000, stage: cause.retryStage });
        }
      }
    }
    finally {
      if (!controller.signal.aborted) {
        try { const result = await request<AccountDetails>(`${endpoint}/${encodeURIComponent(id)}?days=${days}`, { signal: controller.signal }); if (!controller.signal.aborted) setData(result); } catch (cause) { if (!controller.signal.aborted) setError(errorMessage(cause)); }
        if (!controller.signal.aborted) setSyncing(false);
      }
    }
  }
  async function credentialsSaved(result: AuthJSONUpdateResult) {
    setEditorOpen(false); setError(''); setSyncRetry(null);
    // The returned account may represent a newer lifetime. Trust its timestamp
    // instead of attaching the completed operation's timestamp to that identity.
    setData(current => current && { ...current, account: result.account || current.account });
    if (result.refresh_status === 'succeeded') {
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
    <button className='usage-back' onClick={onBack}><ArrowLeft aria-hidden='true' />{admin ? 'Account pool / Accounts' : 'Accounts'}</button>
    {!account || !data ? <><ErrorNotice>{error}</ErrorNotice>{loading ? <Loading label='Loading account…' /> : <button className='button pale' onClick={() => setAttempt(v => v + 1)}>Try again</button>}</> : <>
      <div className='title-row usage-detail-title'><div><h1>{account.account}</h1><p className='subtitle'>{account.plan} <span aria-hidden='true'>·</span> {account.vendor_name}</p></div><div className='usage-detail-actions'><Status account={account} /><button className='button pale' disabled={syncing || loading || syncRetryIn > 0 || !account.current_submission_id} onClick={sync}><RefreshCw className={syncing ? 'usage-spinning' : ''} aria-hidden='true' />{syncing ? 'Updating…' : 'Sync now'}</button></div></div>
      <div className='usage-meta'>{admin && <span className='usage-owner-meta'>User <strong>{account.user_email || '—'}</strong></span>}<span>Account ID <strong>{account.platform_account_id || account.remote_id || '—'}</strong></span><span>Last updated <strong>{formatTime(account.last_success_at, data.timezone)}</strong></span>{account.latest_submission_id && <button className='usage-text-button' onClick={() => onSubmission(account.current_submission_id || account.latest_submission_id)}>View submission<ArrowUpRight aria-hidden='true' /></button>}</div>
      <ErrorNotice>{error}</ErrorNotice>
      {notice && <p className='notice success' role='status'>{notice}</p>}
      {refreshWarning && <p className='notice account-auth-refresh-warning' role='status'>{refreshWarning}</p>}
      {syncRetryIn > 0 && <p className='usage-small' role='status'>{syncRetryLabel(syncRetryIn, syncRetry?.stage)}. Saved account data is retained.</p>}
      {account.last_error && <div className='usage-stale-notice' role='status'><strong>Latest update failed</strong><p>{snapshot ? `Showing the last successful data from ${formatTime(account.last_success_at, data.timezone)}.` : 'No data has been collected for this account yet.'}</p><details><summary>View details</summary><p>{account.last_error}</p><span>Attempted {formatTime(account.last_attempt_at, data.timezone)}</span></details></div>}
      <AuthenticationCard account={account} disabled={syncing || loading} timezone={data.timezone} onEdit={() => setEditorOpen(true)} />
      <div className='usage-metrics'>
        <div><span>Total spent</span><strong>{formatCost(snapshot?.cost_total_usd, 'USD')}</strong><small>{account.last_error && snapshot ? 'Last known cumulative total' : 'Cumulative total'}</small></div>
        <div><span>Input tokens</span><strong>{formatTokens(snapshot?.input_tokens_total, true)}</strong><small>{formatTokens(snapshot?.input_tokens_total)} tokens</small></div>
        <div><span>Output tokens</span><strong>{formatTokens(snapshot?.output_tokens_total, true)}</strong><small>{formatTokens(snapshot?.output_tokens_total)} tokens</small></div>
      </div>
      <div className='usage-windows-panel'><div><UsageWindow label='5-hour usage' value={snapshot?.usage_5h_percent} /><span className='usage-small'>Resets {formatTime(snapshot?.reset_5h_at, data.timezone)}</span></div><div><UsageWindow label='7-day usage' value={snapshot?.usage_7d_percent} /><span className='usage-small'>Resets {formatTime(snapshot?.reset_7d_at, data.timezone)}</span></div></div>
      <section className='usage-trend-panel' aria-labelledby='usage-trend-title'><div className='usage-trend-heading'><div><h2 id='usage-trend-title'>Usage trend</h2><p className='usage-small'>Saved observations, collected every {data.interval_seconds.toLocaleString()} seconds when enabled.</p></div><Select id='usage-date-range' label='Date range' hiddenLabel value={days} onChange={setDays} disabled={syncing || loading} options={[{ value: '7', label: 'Last 7 days' }, { value: '30', label: 'Last 30 days' }, { value: '90', label: 'Last 90 days' }]} /></div>
        <div className='usage-metric-tabs' role='group' aria-label='Trend metric'>{(Object.keys(metricNames) as Metric[]).map(key => <button key={key} aria-pressed={metric === key} onClick={() => setMetric(key)}>{metricNames[key]}</button>)}</div>
        {loading ? <Loading label='Loading usage history…' /> : <UsageTrend key={`${id}:${days}`} data={data} endpoint={`${endpoint}/${encodeURIComponent(id)}/snapshots`} days={days} metric={metric} />}
      </section>
      {editorOpen && <AuthJSONEditor account={account} endpoint={endpoint} onClose={() => setEditorOpen(false)} onSaved={credentialsSaved} />}
    </>}
  </section>;
}
