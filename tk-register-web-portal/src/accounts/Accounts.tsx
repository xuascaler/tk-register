import { ArrowLeft, ArrowUpRight, ChevronLeft, ChevronRight, Clock3, RefreshCw, Search } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { errorMessage, request } from '../api';
import { ErrorNotice, Loading } from '../ui';
import Select from '../submissions/Select';
import { AdminBadge } from '../admin/shared';
import { formatCost, formatPercent, formatTime, formatTokens, knownNumber, statusLabel, statusTone, trendSegments, type Metric } from './helpers';
import type { AccountDetails, AccountPage, AccountSnapshot, SyncedAccount } from './types';

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
  const [error, setError] = useState('');
  useEffect(() => {
    const timer = window.setTimeout(() => { setQuery(search.trim()); setPage(1); }, 300);
    return () => window.clearTimeout(timer);
  }, [search]);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setError('');
    const params = new URLSearchParams({ page: String(page), search: query, status });
    request<AccountPage>(`${endpoint}?${params}`, { signal: controller.signal })
      .then(result => { if (!controller.signal.aborted) setData(result); })
      .catch(cause => { if (!controller.signal.aborted) setError(errorMessage(cause)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [page, query, status, attempt, endpoint]);
  return <section className='usage-accounts'>
    <div className='title-row'><div>{admin && <p className='eyebrow admin-breadcrumb'>Account pool</p>}<h1>Accounts</h1><p className='subtitle'>Account status, usage, and cumulative spend.</p></div>{admin && <AdminBadge />}</div>
    <div className='usage-toolbar'>
      <label className='usage-search'><Search aria-hidden='true' /><span className='sr-only'>Search accounts</span><input value={search} onChange={event => setSearch(event.target.value)} type='search' placeholder={admin ? 'Search account or user email' : 'Search by account email'} maxLength={254} /></label>
      <Select id='usage-account-status' label='Account status' hiddenLabel value={status} onChange={value => { setStatus(value); setPage(1); }} options={[{ value: '', label: 'All account statuses' }, { value: 'active', label: 'Active' }, { value: 'offline', label: 'Offline' }, { value: 'banned', label: 'Banned' }, { value: 'unknown', label: 'Unknown' }]} />
      <button className='button pale usage-icon-button' disabled={loading} aria-label='Refresh saved account data' title='Refresh saved account data' onClick={() => setAttempt(v => v + 1)}><RefreshCw className={loading ? 'usage-spinning' : ''} aria-hidden='true' /></button>
    </div>
    <ErrorNotice>{error}</ErrorNotice>
    {!data && loading ? <Loading label='Loading accounts…' /> : <div className='usage-list-shell' aria-busy={loading}>
      <div className='usage-table-scroll'><table className={`usage-accounts-table ${admin ? 'usage-admin-table' : ''}`}>
        <caption className='sr-only'>{admin ? 'Accounts across all users and their latest successfully collected data' : 'Your accounts and their latest successfully collected data'}</caption>
        <thead><tr><th scope='col'>Account / plan</th>{admin && <th scope='col'>User</th>}<th scope='col'>Status</th><th scope='col'>Usage windows</th><th scope='col' className='usage-total-cell'>Total spent</th><th scope='col'>Last updated</th></tr></thead>
        <tbody>{data?.items.map(account => <tr key={account.id}>
          <td className='usage-account-cell'><button className='usage-email' onClick={() => onOpen(account.id)}>{account.account}<ArrowUpRight aria-hidden='true' /></button><span className='usage-secondary'>{account.plan}</span><span className='usage-small'>Account ID {account.platform_account_id || account.remote_id || '—'}</span></td>
          {admin && <td data-label='User' className='usage-owner-cell'><span className='usage-owner-email'>{account.user_email || '—'}</span></td>}
          <td data-label='Status'><Status account={account} />{account.last_error && account.snapshot && <span className='usage-small'>Last known</span>}</td>
          <td data-label='Usage windows'><UsageWindow label='5 hours' value={account.snapshot?.usage_5h_percent} /><UsageWindow label='7 days' value={account.snapshot?.usage_7d_percent} /></td>
          <td data-label='Total spent' className='usage-total-cell'><strong className='usage-money'>{formatCost(account.snapshot?.cost_total, account.snapshot?.currency)}</strong>{account.last_error && account.snapshot && <span className='usage-small'>Last known total</span>}</td>
          <td data-label='Last updated'><span className='usage-date'>{formatTime(account.last_success_at, undefined, true)}</span>{account.last_error ? <button className='usage-error-link' onClick={() => onOpen(account.id)}>Update failed</button> : !account.snapshot ? <span className='usage-small'>No data collected</span> : null}</td>
        </tr>)}</tbody>
      </table></div>
      {!data?.items.length && <div className='usage-empty'><h2>{query || status ? 'No matching accounts' : 'No accounts yet'}</h2><p>{query || status ? 'Try another email or account status.' : admin ? 'Submitted accounts from all users will appear here with their latest available data.' : 'Your submitted accounts will appear here with their latest available data.'}</p></div>}
      {data && data.total > 0 && <div className='usage-pagination'><span>{data.total.toLocaleString()} {data.total === 1 ? 'account' : 'accounts'} · Page {data.page} of {Math.max(1, Math.ceil(data.total / data.page_size))}</span><div><button className='button pale' disabled={loading || page <= 1} onClick={() => setPage(p => p - 1)}><ChevronLeft aria-hidden='true' />Previous</button><button className='button pale' disabled={loading || page * data.page_size >= data.total} onClick={() => setPage(p => p + 1)}>Next<ChevronRight aria-hidden='true' /></button></div></div>}
    </div>}
    <p className='usage-footnote'><Clock3 aria-hidden='true' />Figures show the last successful collection. Missing values are shown as “—”.</p>
  </section>;
}

const metricNames: Record<Metric, string> = { cost_total: 'Total spent', input_tokens_total: 'Input tokens', output_tokens_total: 'Output tokens' };

function Trend({ snapshots, interval, timezone, metric }: { snapshots: AccountSnapshot[]; interval: number; timezone: string; metric: Metric }) {
  const target = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(640);
  const [table, setTable] = useState(false);
  useEffect(() => {
    const element = target.current;
    if (!element) return;
    const observer = new ResizeObserver(entries => setWidth(Math.max(240, entries[0].contentRect.width)));
    observer.observe(element); return () => observer.disconnect();
  }, []);
  const segments = trendSegments(snapshots, metric, interval);
  const currencies = [...new Set(segments.flat().map(p => p.snapshot.currency).filter(Boolean))];
  const mixedCurrency = metric === 'cost_total' && currencies.length > 1;
  // Keep separate currencies out of a shared numeric axis. The data table retains all observations.
  const currency = currencies.at(-1) || null;
  const displayed = metric === 'cost_total' ? segments.map(segment => segment.filter(p => p.snapshot.currency === currency)).filter(segment => segment.length) : segments;
  const points = displayed.flat();
  const height = 260, left = width < 420 ? 56 : 72, right = 20, top = 24, bottom = 44;
  const first = Math.min(...points.map(p => p.time)), last = Math.max(...points.map(p => p.time));
  const min = Math.min(...points.map(p => p.value)), max = Math.max(...points.map(p => p.value));
  const span = Math.max(1, max - min), lo = Math.max(0, min - span * .12), hi = max + span * .12;
  const x = (time: number) => left + (last === first ? .5 : (time - first) / (last - first)) * (width - left - right);
  const y = (value: number) => top + (1 - (value - lo) / (hi - lo)) * (height - top - bottom);
  const valueLabel = (value: number | null, unit = currency) => metric === 'cost_total' ? formatCost(value, unit) : formatTokens(value);
  const ticks = width < 500 ? 3 : 4;
  return <div ref={target} className='usage-trend'>
    <div className='usage-chart-unit'>{metric === 'cost_total' ? currency ? `Cumulative spend · ${currency}` : 'Cumulative spend' : `Cumulative ${metric === 'input_tokens_total' ? 'input' : 'output'} tokens`}</div>
    {!points.length ? <div className='usage-chart-empty'><h3>No usage data yet</h3><p>A trend will appear after successful collections.</p></div> : <svg className='usage-chart' width={width} height={height} viewBox={`0 0 ${width} ${height}`} role='img' aria-label={`${metricNames[metric]} over time, ${points.length} collected observations. Gaps indicate unavailable data or a counter reset.`}>
      {[0, 1, 2, 3].map(index => { const value = lo + (hi - lo) * index / 3; const pos = y(value); return <g key={index}><line x1={left} y1={pos} x2={width - right} y2={pos} className='usage-gridline' /><text x={left - 10} y={pos + 4} textAnchor='end'>{new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 }).format(value)}</text></g>; })}
      {Array.from({ length: first === last ? 1 : ticks }, (_, index) => { const time = first === last ? first : first + (last - first) * index / (ticks - 1); return <text key={index} x={x(time)} y={height - 16} textAnchor={first === last ? 'middle' : index === 0 ? 'start' : index === ticks - 1 ? 'end' : 'middle'}>{new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', timeZone: timezone }).format(time)}</text>; })}
      {displayed.map((segment, index) => <g key={index}><polyline fill='none' className='usage-chart-line' points={segment.map(point => `${x(point.time)},${y(point.value)}`).join(' ')} />{segment.map(point => <circle key={point.snapshot.id} cx={x(point.time)} cy={y(point.value)} r={points.length > 45 ? 2 : 3.5} className='usage-chart-point'><title>{formatTime(point.snapshot.collected_at, timezone)}: {valueLabel(point.value)}</title></circle>)}</g>)}
    </svg>}
    <div className='usage-chart-bottom'><span>{mixedCurrency ? `Chart shows ${currency}; other currencies remain in the data table. ` : ''}Collection times use {timezone}. Gaps are not zero usage.</span><button className='usage-text-button' onClick={() => setTable(v => !v)} aria-expanded={table}>{table ? 'Hide data' : 'View data'}</button></div>
    {table && <div className='usage-data-scroll'><table className='usage-data-table'><caption className='sr-only'>Collected usage values</caption><thead><tr><th scope='col'>Collected at</th><th scope='col'>{metricNames[metric]}</th></tr></thead><tbody>{snapshots.map(row => <tr key={row.id}><td>{formatTime(row.collected_at, timezone)}</td><td>{valueLabel(row[metric], row.currency)}</td></tr>)}</tbody></table>{!snapshots.length && <p className='usage-small'>No observations in this date range.</p>}</div>}
  </div>;
}

export function AccountUsageDetails({ id, onBack, onSubmission, admin = false }: { id: string; onBack: () => void; onSubmission: (id: string) => void; admin?: boolean }) {
  const endpoint = admin ? '/admin/synced-accounts' : '/accounts';
  const [data, setData] = useState<AccountDetails | null>(null);
  const [days, setDays] = useState('7');
  const [metric, setMetric] = useState<Metric>('cost_total');
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [attempt, setAttempt] = useState(0);
  const pending = useRef<AbortController | null>(null);
  useEffect(() => () => pending.current?.abort(), [id]);
  useEffect(() => {
    const controller = new AbortController(); setLoading(true); setError('');
    request<AccountDetails>(`${endpoint}/${encodeURIComponent(id)}?days=${days}`, { signal: controller.signal })
      .then(result => { if (!controller.signal.aborted) setData(result); })
      .catch(cause => { if (!controller.signal.aborted) setError(errorMessage(cause)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [id, days, attempt, endpoint]);
  async function sync() {
    if (syncing) return;
    const controller = new AbortController(); pending.current = controller;
    setSyncing(true); setError(''); setNotice('');
    try {
      const result = await request<{ account: SyncedAccount }>(`${endpoint}/${encodeURIComponent(id)}/sync`, { method: 'POST', body: {}, signal: controller.signal });
      if (!controller.signal.aborted) {
        if (result.account.last_error) setError(result.account.last_error);
        else setNotice('Account data updated.');
      }
    } catch (cause) { if (!controller.signal.aborted) setError(errorMessage(cause)); }
    finally {
      if (!controller.signal.aborted) {
        try { const result = await request<AccountDetails>(`${endpoint}/${encodeURIComponent(id)}?days=${days}`, { signal: controller.signal }); if (!controller.signal.aborted) setData(result); } catch (cause) { if (!controller.signal.aborted) setError(errorMessage(cause)); }
        if (!controller.signal.aborted) setSyncing(false);
      }
    }
  }
  const account = data?.account, snapshot = account?.snapshot;
  return <section className='usage-detail'>
    <button className='usage-back' onClick={onBack}><ArrowLeft aria-hidden='true' />{admin ? 'Account pool / Accounts' : 'Accounts'}</button>
    {!account || !data ? <><ErrorNotice>{error}</ErrorNotice>{loading ? <Loading label='Loading account…' /> : <button className='button pale' onClick={() => setAttempt(v => v + 1)}>Try again</button>}</> : <>
      <div className='title-row usage-detail-title'><div><h1>{account.account}</h1><p className='subtitle'>{account.plan} <span aria-hidden='true'>·</span> {account.vendor_name}</p></div><div className='usage-detail-actions'><Status account={account} /><button className='button pale' disabled={syncing || loading || !account.current_submission_id} onClick={sync}><RefreshCw className={syncing ? 'usage-spinning' : ''} aria-hidden='true' />{syncing ? 'Updating…' : 'Sync now'}</button></div></div>
      <div className='usage-meta'>{admin && <span className='usage-owner-meta'>User <strong>{account.user_email || '—'}</strong></span>}<span>Account ID <strong>{account.platform_account_id || account.remote_id || '—'}</strong></span><span>Last updated <strong>{formatTime(account.last_success_at, data.timezone)}</strong></span>{account.latest_submission_id && <button className='usage-text-button' onClick={() => onSubmission(account.current_submission_id || account.latest_submission_id)}>View submission<ArrowUpRight aria-hidden='true' /></button>}</div>
      <ErrorNotice>{error}</ErrorNotice>
      {notice && <p className='notice success' role='status'>{notice}</p>}
      {account.last_error && <div className='usage-stale-notice' role='status'><strong>Latest update failed</strong><p>{snapshot ? `Showing the last successful data from ${formatTime(account.last_success_at, data.timezone)}.` : 'No data has been collected for this account yet.'}</p><details><summary>View details</summary><p>{account.last_error}</p><span>Attempted {formatTime(account.last_attempt_at, data.timezone)}</span></details></div>}
      <div className='usage-metrics'>
        <div><span>Total spent</span><strong>{formatCost(snapshot?.cost_total, snapshot?.currency)}</strong><small>{account.last_error && snapshot ? 'Last known cumulative total' : 'Cumulative total'}</small></div>
        <div><span>Input tokens</span><strong>{formatTokens(snapshot?.input_tokens_total, true)}</strong><small>{formatTokens(snapshot?.input_tokens_total)} tokens</small></div>
        <div><span>Output tokens</span><strong>{formatTokens(snapshot?.output_tokens_total, true)}</strong><small>{formatTokens(snapshot?.output_tokens_total)} tokens</small></div>
      </div>
      <div className='usage-windows-panel'><div><UsageWindow label='5-hour usage' value={snapshot?.usage_5h_percent} /><span className='usage-small'>Resets {formatTime(snapshot?.reset_5h_at, data.timezone)}</span></div><div><UsageWindow label='7-day usage' value={snapshot?.usage_7d_percent} /><span className='usage-small'>Resets {formatTime(snapshot?.reset_7d_at, data.timezone)}</span></div></div>
      <section className='usage-trend-panel' aria-labelledby='usage-trend-title'><div className='usage-trend-heading'><div><h2 id='usage-trend-title'>Usage trend</h2><p className='usage-small'>Saved observations, collected every {data.interval_hours} {data.interval_hours === 1 ? 'hour' : 'hours'} when enabled.</p></div><Select id='usage-date-range' label='Date range' hiddenLabel value={days} onChange={setDays} disabled={syncing} options={[{ value: '7', label: 'Last 7 days' }, { value: '30', label: 'Last 30 days' }, { value: '90', label: 'Last 90 days' }]} /></div>
        <div className='usage-metric-tabs' role='group' aria-label='Trend metric'>{(Object.keys(metricNames) as Metric[]).map(key => <button key={key} aria-pressed={metric === key} onClick={() => setMetric(key)}>{metricNames[key]}</button>)}</div>
        {loading ? <Loading label='Loading usage history…' /> : <Trend snapshots={data.snapshots} interval={data.interval_hours} timezone={data.timezone} metric={metric} />}
      </section>
    </>}
  </section>;
}
