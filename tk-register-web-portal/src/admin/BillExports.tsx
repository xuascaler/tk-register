import { AlertCircle, Check, ChevronDown, Download, FileSpreadsheet, LoaderCircle, Search, X } from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from 'react';
import { APIError, errorMessage, request } from '../api';
import Select from '../submissions/Select';
import { dateTime, Empty } from '../submissions/shared';
import { ErrorNotice, Loading } from '../ui';
import { Pagination } from './shared';
import type { AdminUser, Page } from './types';
import { beijingDate, billAmount, exportDateError, exportRunning, offsetDate, type BillExportJob, type BillExportResults, type BillExportReview, type BillExportSummary, type BillExportUser, type BillExportView } from './bill-exports';
import './bill-exports.css';

function UserPicker({ value, onChange, disabled, invalid }: {
  value: BillExportUser | null; onChange: (value: BillExportUser | null) => void; disabled: boolean; invalid: boolean;
}) {
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const [search, setSearch] = useState(value?.email || '');
  const [query, setQuery] = useState(search);
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<AdminUser[]>([]);
  const [page, setPage] = useState(1);
  const [total, setTotal] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [active, setActive] = useState(0);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => { if (value) setSearch(value.email); }, [value?.id, value?.email]);
  useEffect(() => {
    const timer = window.setTimeout(() => { setQuery(search.trim()); setPage(1); setActive(0); }, 250);
    return () => window.clearTimeout(timer);
  }, [search]);
  useEffect(() => {
    if (!open || disabled || query !== search.trim()) return;
    const controller = new AbortController();
    setBusy(true); setError('');
    request<Page<AdminUser>>(`/admin/users?${new URLSearchParams({ search: query, page: String(page) })}`, { signal: controller.signal })
      .then(result => {
        if (controller.signal.aborted) return;
        setItems(previous => page === 1 ? result.items : [...previous, ...result.items.filter(item => !previous.some(saved => saved.id === item.id))]);
        setTotal(result.total);
      })
      .catch(cause => { if (!controller.signal.aborted) { setError(errorMessage(cause)); if (page === 1) setItems([]); } })
      .finally(() => { if (!controller.signal.aborted) setBusy(false); });
    return () => controller.abort();
  }, [open, query, search, page, attempt, disabled]);
  useEffect(() => {
    const outside = (event: Event) => { if (event.target instanceof Node && !root.current?.contains(event.target)) setOpen(false); };
    document.addEventListener('pointerdown', outside);
    document.addEventListener('focusin', outside);
    return () => { document.removeEventListener('pointerdown', outside); document.removeEventListener('focusin', outside); };
  }, []);
  useEffect(() => { root.current?.querySelector(`#bill-user-option-${active}`)?.scrollIntoView({ block: 'nearest' }); }, [active]);
  function choose(user: AdminUser) { onChange({ id: String(user.id), email: user.email }); setSearch(user.email); setOpen(false); input.current?.focus(); }
  function keys(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'Escape') { setOpen(false); return; }
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault(); setOpen(true);
      setActive(current => !open ? 0 : Math.max(0, Math.min(items.length - 1, current + (event.key === 'ArrowDown' ? 1 : -1))));
    }
    if (event.key === 'Enter' && open) { event.preventDefault(); if (!busy && items[active]) choose(items[active]); }
  }
  return <div className='bill-user-picker' ref={root}>
    <label htmlFor='bill-export-user'>User</label>
    <div className='bill-user-control'><Search aria-hidden='true' /><input ref={input} id='bill-export-user' type='text' role='combobox' autoComplete='off' spellCheck={false} autoCapitalize='none' placeholder='Search user email' value={search} disabled={disabled} aria-expanded={open && !disabled} aria-controls='bill-user-listbox' aria-autocomplete='list' aria-activedescendant={open && items[active] && !busy ? `bill-user-option-${active}` : undefined} aria-invalid={invalid} onFocus={() => setOpen(true)} onKeyDown={keys} onChange={event => { setSearch(event.target.value); setItems([]); setBusy(true); onChange(null); setOpen(true); }} />
      {value ? <button type='button' disabled={disabled} aria-label='Clear selected user' onClick={() => { setSearch(''); onChange(null); setOpen(true); input.current?.focus(); }}><X aria-hidden='true' /></button> : <ChevronDown className='bill-user-chevron' aria-hidden='true' />}
    </div>
    {open && !disabled && <div className='bill-user-menu'>
      <div role='listbox' id='bill-user-listbox' aria-label='Users' aria-busy={busy}>
        {items.map((item, index) => <button type='button' tabIndex={-1} role='option' id={`bill-user-option-${index}`} key={item.id} aria-selected={String(item.id) === value?.id} className={index === active ? 'is-active' : ''} onPointerMove={() => setActive(index)} onMouseDown={event => event.preventDefault()} onClick={() => choose(item)}><span>{item.email}<small>User #{item.id}</small></span>{String(item.id) === value?.id && <Check aria-hidden='true' />}</button>)}
      </div>
      {busy ? <p className='bill-user-message' role='status'><LoaderCircle className='spin' aria-hidden='true' />Loading users…</p> : error ? <div className='bill-user-message'><span role='alert'>{error}</span><button type='button' className='text-button' onClick={() => setAttempt(v => v + 1)}>Try again</button></div> : !items.length ? <p className='bill-user-message'>No matching users.</p> : items.length < total ? <button type='button' className='bill-users-more' onClick={() => setPage(v => v + 1)}>Load more users</button> : null}
    </div>}
  </div>;
}

const viewNames: Record<BillExportView, string> = { summary: 'Account summary', details: 'Bill details', review: 'Needs review' };
const phaseNames: Record<string, string> = { queued: 'Queued', fetching: 'Fetching bill', filtering: 'Preparing export', completed: 'Ready', failed: 'Failed', expired: 'Expired' };

function ExportResults({ job, view, onView }: { job: BillExportJob; view: BillExportView; onView: (view: BillExportView) => void }) {
  const [result, setResult] = useState<BillExportResults | null>(null);
  const [resultView, setResultView] = useState(view);
  const [page, setPage] = useState(1);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  useEffect(() => { setPage(1); setResult(null); }, [view, job.id]);
  useEffect(() => {
    const controller = new AbortController();
    setBusy(true); setError(''); setResult(null);
    request<BillExportResults>(`/admin/bill-exports/${encodeURIComponent(job.id)}/results?${new URLSearchParams({ view, page: String(page) })}`, { signal: controller.signal })
      .then(value => { if (!controller.signal.aborted) { setResult(value); setResultView(view); } })
      .catch(cause => { if (!controller.signal.aborted) setError(errorMessage(cause)); })
      .finally(() => { if (!controller.signal.aborted) setBusy(false); });
    return () => controller.abort();
  }, [job.id, view, page, attempt]);
  function changeView(value: BillExportView) { setPage(1); onView(value); }
  function tabKey(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const views = Object.keys(viewNames) as BillExportView[];
    const next = event.key === 'ArrowRight' ? (index + 1) % views.length : event.key === 'ArrowLeft' ? (index + views.length - 1) % views.length : event.key === 'Home' ? 0 : event.key === 'End' ? views.length - 1 : -1;
    if (next < 0) return;
    event.preventDefault(); changeView(views[next]); document.getElementById(`bill-tab-${views[next]}`)?.focus();
  }
  return <div className='bill-result-data'>
    <div className='tabs bill-result-tabs' role='tablist' aria-label='Export results'>{(Object.keys(viewNames) as BillExportView[]).map((name, index) => <button id={`bill-tab-${name}`} key={name} type='button' role='tab' aria-selected={view === name} aria-controls='bill-result-panel' tabIndex={view === name ? 0 : -1} onKeyDown={event => tabKey(event, index)} onClick={() => changeView(name)}>{viewNames[name]}<span className={`tab-count ${name === 'review' && job.review_account_count ? 'bill-review-count' : ''}`}>{name === 'summary' ? job.account_count : name === 'details' ? job.row_count : job.review_account_count}</span></button>)}</div>
    <div id='bill-result-panel' role='tabpanel' aria-labelledby={`bill-tab-${view}`} aria-busy={busy}>
      <ErrorNotice>{error}</ErrorNotice>
      {busy || (result !== null && resultView !== view) ? <Loading label={`Loading ${viewNames[view].toLowerCase()}…`} /> : error ? <button type='button' className='button secondary bill-result-retry' onClick={() => setAttempt(v => v + 1)}>Try again</button> : result && <>
        {!result.items.length ? <Empty title={view === 'review' ? 'No accounts need review' : 'No bill records'}>{view === 'review' ? 'All selected accounts have a usable account mapping.' : 'No bill rows were returned for this user’s accounts in the selected period.'}</Empty> : <div className='admin-table-wrap bill-table-wrap'>
          {view === 'summary' ? <table className='admin-table bill-summary-table'><caption className='sr-only'>Selected user account billing summary</caption><thead><tr><th scope='col'>Account / plan</th><th scope='col'>Platform ID</th><th scope='col'>Bill rows</th><th scope='col'>Amount (CNY)</th><th scope='col'>Result</th></tr></thead><tbody>{(result.items as BillExportSummary[]).map((item, index) => <tr key={`${item.account}-${index}`}><td className='admin-primary-cell' data-label='Account / plan'><span className='bill-account-email'>{item.account}</span><span className='admin-cell-note'>{item.plan}</span></td><td data-label='Platform ID'>{item.platform_account_id || '—'}</td><td data-label='Bill rows' className='bill-number'>{item.row_count}</td><td data-label='Amount (CNY)' className='bill-number bill-amount'>{billAmount(item.amount_cny)}</td><td data-label='Result'><span className={`bill-result-label ${item.status === 'needs_review' ? 'is-review' : item.status === 'matched' ? 'is-matched' : ''}`}>{item.status === 'matched' ? 'Matched' : item.status === 'no_bills' ? 'No bill rows' : 'Needs review'}</span>{item.reason && <span className='admin-cell-note'>{item.reason}</span>}</td></tr>)}</tbody></table>
            : view === 'details' ? <table className='bill-details-table'><caption className='sr-only'>Filtered upstream bill details; amounts in CNY</caption><thead><tr>{result.columns?.map((column, index) => <th scope='col' key={`${column}-${index}`}>{column}</th>)}</tr></thead><tbody>{(result.items as string[][]).map((row, index) => <tr key={index}>{row.map((cell, column) => <td key={column}>{cell || '—'}</td>)}</tr>)}</tbody></table>
              : <table className='admin-table bill-review-table'><caption className='sr-only'>Accounts requiring review before their bills can be matched</caption><thead><tr><th scope='col'>Account</th><th scope='col'>Platform ID</th><th scope='col'>Reason</th></tr></thead><tbody>{(result.items as BillExportReview[]).map((item, index) => <tr key={`${item.account}-${index}`}><td className='admin-primary-cell' data-label='Account'><span className='bill-account-email'>{item.account}</span></td><td data-label='Platform ID'>{item.platform_account_id || '—'}</td><td data-label='Reason'>{item.reason}</td></tr>)}</tbody></table>}
        </div>}
        <Pagination data={result} noun={view === 'details' ? 'bill row' : 'account'} busy={busy} onPage={setPage} />
      </>}
    </div>
  </div>;
}

export default function BillExports() {
  const today = beijingDate();
  const [user, setUser] = useState<BillExportUser | null>(null);
  const [start, setStart] = useState(offsetDate(today, -6));
  const [end, setEnd] = useState(today);
  const [job, setJob] = useState<BillExportJob | null>(null);
  const [recent, setRecent] = useState<BillExportJob[]>([]);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [error, setError] = useState('');
  const [recentError, setRecentError] = useState('');
  const [pollError, setPollError] = useState('');
  const [downloadError, setDownloadError] = useState('');
  const [validation, setValidation] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [pollAttempt, setPollAttempt] = useState(0);
  const [view, setView] = useState<BillExportView>('summary');
  const pending = useRef<AbortController | null>(null);
  const downloadingRequest = useRef<AbortController | null>(null);
  const submitting = useRef(false);
  const touched = useRef(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; pending.current?.abort(); downloadingRequest.current?.abort(); };
  }, []);
  useEffect(() => {
    const controller = new AbortController(); setLoading(true); setRecentError('');
    request<{ items: BillExportJob[] }>('/admin/bill-exports', { signal: controller.signal }).then(result => {
      if (controller.signal.aborted) return;
      setRecent(result.items);
      if (!touched.current && result.items[0]) {
        const latest = result.items[0];
        setJob(latest); setUser(latest.user); setStart(latest.start_date); setEnd(latest.end_date);
      }
    }).catch(cause => { if (!controller.signal.aborted) setRecentError(errorMessage(cause)); }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [attempt]);
  const running = exportRunning(job);
  useEffect(() => {
    if (!job || !running) return;
    const id = job.id;
    const controller = new AbortController();
    let timer = 0;
    let failures = 0;
    setPollError('');
    const poll = async () => {
      try {
        const latest = await request<BillExportJob>(`/admin/bill-exports/${encodeURIComponent(id)}`, { signal: controller.signal });
        if (controller.signal.aborted) return;
        failures = 0; setPollError(''); setJob(latest);
        setRecent(previous => previous.map(item => item.id === latest.id ? latest : item));
        if (exportRunning(latest)) timer = window.setTimeout(poll, 2000);
      } catch (cause) {
        if (controller.signal.aborted) return;
        failures += 1;
        if (failures >= 3) setPollError(errorMessage(cause));
        else timer = window.setTimeout(poll, failures * 3000);
      }
    };
    timer = window.setTimeout(poll, 1000);
    return () => { controller.abort(); window.clearTimeout(timer); };
  }, [job?.id, running, pollAttempt]);
  function changed() { touched.current = true; downloadingRequest.current?.abort(); setDownloading(false); setJob(null); setError(''); setDownloadError(''); setValidation(false); setView('summary'); }
  function chooseRecent(id: string) {
    const selected = recent.find(item => item.id === id);
    if (!selected) return;
    touched.current = true; downloadingRequest.current?.abort(); setDownloading(false); setJob(selected); setUser(selected.user); setStart(selected.start_date); setEnd(selected.end_date); setView('summary'); setError(''); setDownloadError(''); setValidation(false);
  }
  function shortcut(days: number, yesterday = false) {
    changed(); const last = yesterday ? offsetDate(today, -1) : today; setEnd(last); setStart(offsetDate(last, -(days - 1)));
  }
  async function generate(event: FormEvent) {
    event.preventDefault();
    if (submitting.current) return;
    setValidation(true); setError(''); setDownloadError('');
    const problem = exportDateError(start, end);
    if (!user || problem) { setError(!user ? 'Select a user from the search results.' : problem); return; }
    touched.current = true; submitting.current = true; setCreating(true);
    const controller = new AbortController(); pending.current = controller;
    try {
      const result = await request<BillExportJob>('/admin/bill-exports', { method: 'POST', body: { user_id: String(user.id), start_date: start, end_date: end }, signal: controller.signal });
      if (controller.signal.aborted) return;
      setJob(result); setRecent(previous => [result, ...previous.filter(item => item.id !== result.id)].slice(0, 20)); setView('summary'); setValidation(false);
    } catch (cause) { if (!controller.signal.aborted) setError(errorMessage(cause)); }
    finally { submitting.current = false; if (mounted.current) setCreating(false); }
  }
  async function download() {
    if (!job || job.status !== 'completed' || downloading) return;
    const controller = new AbortController(); downloadingRequest.current = controller; setDownloading(true); setDownloadError('');
    try {
      const response = await fetch(`/tk-register/api/admin/bill-exports/${encodeURIComponent(job.id)}/download`, { credentials: 'same-origin', cache: 'no-store', signal: controller.signal });
      if (!response.ok) {
        const body = await response.json().catch(() => null);
        if (response.status === 401) window.dispatchEvent(new Event('tk-session-expired'));
        throw new APIError(body?.message || 'The export could not be downloaded. Please try again.', response.status);
      }
      const blob = await response.blob();
      if (controller.signal.aborted) return;
      const url = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = url; link.download = `claude-bills-user-${job.user.id}-${job.start_date}-${job.end_date}.xlsx`;
      document.body.appendChild(link); link.click(); link.remove(); window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (cause) { if (!controller.signal.aborted) setDownloadError(errorMessage(cause)); }
    finally { if (mounted.current) setDownloading(false); }
  }
  const locked = creating || running;
  return <section className='bill-exports'>
    <div className='section-heading bill-export-heading'><div><h2>Claude bill exports</h2><p className='muted small'>Export a user’s account bills for a selected date range.</p></div><span className='bill-vendor'>TK Register 0</span></div>
    <form className='panel bill-export-form' onSubmit={generate} noValidate>
      <div className='bill-export-fields'>
        <UserPicker value={user} disabled={locked} invalid={validation && !user} onChange={value => { changed(); setUser(value); }} />
        <div className='bill-date-field'><label htmlFor='bill-start-date'>Start date</label><input id='bill-start-date' type='date' value={start} max={end || today} disabled={locked} aria-invalid={validation && !!exportDateError(start, end)} aria-describedby='bill-date-help' onChange={event => { changed(); setStart(event.target.value); }} /></div>
        <div className='bill-date-field'><label htmlFor='bill-end-date'>End date</label><input id='bill-end-date' type='date' value={end} min={start || undefined} max={today} disabled={locked} aria-invalid={validation && !!exportDateError(start, end)} aria-describedby='bill-date-help' onChange={event => { changed(); setEnd(event.target.value); }} /></div>
      </div>
      <div className='bill-date-options'><p id='bill-date-help'>Beijing time · Up to 60 days, including both dates.</p><div className='bill-shortcuts' aria-label='Date shortcuts'><button type='button' disabled={locked} onClick={() => shortcut(1, true)}>Yesterday</button><button type='button' disabled={locked} onClick={() => shortcut(7)}>Last 7 days</button><button type='button' disabled={locked} onClick={() => shortcut(30)}>Last 30 days</button></div></div>
      <ErrorNotice>{error}</ErrorNotice>
      <div className='bill-form-footer'><p>Includes only accounts currently assigned to the selected user.</p><button type='submit' className='button primary' disabled={locked}>{creating || running ? <LoaderCircle className='spin' aria-hidden='true' /> : <FileSpreadsheet aria-hidden='true' />}{creating ? 'Creating export…' : running ? 'Export in progress' : 'Generate export'}</button></div>
    </form>
    {recent.length > 0 && <div className='bill-recent'><Select id='bill-recent-export' label='Recent exports' value={job?.id || ''} placeholder='Choose a previous export' disabled={creating} onChange={chooseRecent} options={recent.map(item => ({ value: item.id, label: `${item.user.email} · ${item.start_date} – ${item.end_date} · ${phaseNames[item.status] || item.status}` }))} /></div>}
    {recentError && <div className='bill-recent-error'><p className='muted small'>Recent exports could not be loaded. {recentError}</p><button type='button' className='button quiet' disabled={loading} onClick={() => setAttempt(v => v + 1)}>Try again</button></div>}
    {job ? <section className='panel bill-export-result' aria-label='Bill export'>
      <div className='bill-result-heading'><div><div className='bill-result-title'><h2>{job.user.email}</h2><span className={`bill-job-status is-${job.status}`}>{job.status === 'completed' ? <Check aria-hidden='true' /> : job.status === 'failed' || job.status === 'expired' ? <AlertCircle aria-hidden='true' /> : <LoaderCircle className='spin' aria-hidden='true' />}{phaseNames[job.status] || job.status}</span></div><p className='muted small'>{job.start_date} – {job.end_date} · Beijing time{job.finished_at ? ` · Generated ${dateTime(job.finished_at)}` : ''}</p>{job.status === 'completed' && <p className='bill-download-expiry'>{job.expires_at ? `Available until ${dateTime(job.expires_at)}` : 'Downloads are available for 7 days.'}</p>}</div>{job.status === 'completed' && <button type='button' className='button secondary' onClick={download} disabled={downloading}>{downloading ? <LoaderCircle className='spin' aria-hidden='true' /> : <Download aria-hidden='true' />}{downloading ? 'Downloading…' : 'Download Excel'}</button>}</div>
      {downloadError && <div className='bill-result-notice'><ErrorNotice>{downloadError}</ErrorNotice></div>}
      {running ? <div className='bill-export-progress' role='status'><ol>{['queued', 'fetching', 'filtering'].map((phase, index) => { const current = ['queued', 'fetching', 'filtering'].indexOf(job.status); return <li className={index < current ? 'is-complete' : index === current ? 'is-current' : ''} key={phase}><span>{index < current ? <Check aria-hidden='true' /> : index + 1}</span>{phaseNames[phase]}</li>; })}</ol><p>{job.status === 'queued' ? 'Your export is queued. It will start when the supplier connection is available.' : job.status === 'fetching' ? 'Fetching the supplier’s Claude bill for your selected dates.' : 'Matching the selected user’s accounts and preparing the Excel file.'}</p><p className='muted small'>You can leave this page and find the result in Recent exports.</p>{pollError && <div className='bill-poll-error'><ErrorNotice>{pollError}</ErrorNotice><button type='button' className='button secondary' onClick={() => setPollAttempt(v => v + 1)}>Check status</button></div>}</div>
        : job.status === 'failed' || job.status === 'expired' ? <div className='bill-export-failed'><AlertCircle aria-hidden='true' /><div><h3>{job.status === 'expired' ? 'This export has expired' : 'Export could not be generated'}</h3><p>{job.status === 'expired' ? 'Export files are available for 7 days. Generate a new export to download these bills again.' : job.error || 'The supplier bill could not be retrieved. Please try again.'}</p>{job.status === 'failed' && <p className='muted small'>Review the message, then generate a new export.</p>}</div></div>
          : <>
            <div className='bill-export-metrics'><div><span>Selected accounts</span><strong>{job.account_count}</strong></div><div><span>Accounts with bills</span><strong>{job.matched_account_count}</strong></div><div><span>Bill rows</span><strong>{job.row_count}</strong></div><div className='bill-total'><span>Period total · CNY</span><strong>{billAmount(job.total_cny)}</strong></div></div>
            {!!job.review_account_count && <div className='bill-review-notice'><AlertCircle aria-hidden='true' /><p>{job.review_account_count} {job.review_account_count === 1 ? 'account needs' : 'accounts need'} review. Unmatched amounts are not included in the total.</p><button type='button' onClick={() => setView('review')}>View accounts</button></div>}
            <ExportResults key={job.id} job={job} view={view} onView={setView} />
          </>}
    </section> : loading ? <Loading label='Loading recent exports…' /> : <div className='panel bill-export-empty'><FileSpreadsheet aria-hidden='true' /><h3>Prepare a Claude bill export</h3><p>Choose a user and dates to see account totals, bill details, and any accounts that need review.</p></div>}
  </section>;
}
