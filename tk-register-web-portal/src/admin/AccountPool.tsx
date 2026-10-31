import { ArrowRight, RefreshCw } from 'lucide-react';
import { useEffect, useState } from 'react';
import { errorMessage, request } from '../api';
import { ErrorNotice, Loading } from '../ui';
import { Back, dateTime, Empty, StateBadge } from '../submissions/shared';
import type { StatusRefresh } from '../submissions/status';
import type { Submission } from '../submissions/types';
import type { AccountDetail, AdminAccount, AdminSubmission, Page } from './types';
import { AdminBadge, CurrentAccountStatus, ListToolbar, Pagination, SubmissionTable, syncCurrentStatuses, useListFilters } from './shared';

function updateAccount(account: AdminAccount, id: string, submission: Submission): AdminAccount {
  return {
    ...account,
    latest_submission: account.latest_submission.id === id ? submission : account.latest_submission,
    current_submission: account.current_submission?.id === id ? submission : account.current_submission,
  };
}
function updateHistory(history: Page<AdminSubmission>, id: string, submission: Submission): Page<AdminSubmission> {
  return { ...history, items: history.items.map(item => ({
    ...item,
    ...(item.id === id ? submission : {}),
    current_submission: item.current_submission?.id === id ? submission : item.current_submission,
  })) };
}

export function Accounts({ onOpen }: { onOpen: (id: string) => void }) {
  const filters = useListFilters();
  const { page, query, status, attempt, forceNext } = filters;
  const [list, setList] = useState<Page<AdminAccount> | null>(null);
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [error, setError] = useState('');
  const [refreshes, setRefreshes] = useState<Record<string, StatusRefresh>>({});
  useEffect(() => {
    const abort = new AbortController();
    const force = forceNext.current; forceNext.current = false;
    setLoading(true); setError(''); setRefreshes({}); setSyncing(false);
    const params = new URLSearchParams({ page: String(page), search: query, status });
    request<Page<AdminAccount>>(`/admin/accounts?${params}`, { signal: abort.signal })
      .then(async data => {
        if (abort.signal.aborted) return;
        setList(data); setLoading(false); setSyncing(true);
        await syncCurrentStatuses(data.items.map(item => item.current_submission), abort.signal, force,
          (id, state) => setRefreshes(current => abort.signal.aborted ? current : { ...current, [id]: state }),
          (id, submission) => setList(current => !current || abort.signal.aborted ? current : {
            ...current, items: current.items.map(account => updateAccount(account, id, submission)),
          }),
        );
      })
      .catch(cause => { if (!abort.signal.aborted) setError(errorMessage(cause)); })
      .finally(() => { if (!abort.signal.aborted) { setLoading(false); setSyncing(false); } });
    return () => abort.abort();
  }, [page, query, status, attempt, forceNext]);
  return <div className='admin-page'>
    <div className='title-row'><div><p className='eyebrow admin-breadcrumb'>Account pool</p><h1>Accounts</h1><p className='subtitle'>All submitted accounts, grouped by owner and vendor.</p></div><AdminBadge /></div>
    <ListToolbar id='accounts' search={filters.search} onSearch={filters.setSearch} status={status} onStatus={filters.changeStatus} busy={loading || syncing} onRefresh={filters.refresh} />
    <ErrorNotice>{error}</ErrorNotice>
    {loading && !list ? <Loading label='Loading accounts…' /> : list ? <section className='panel admin-table-panel' aria-busy={loading}>
      {!list.items.length ? <Empty title={query || status ? 'No matching accounts' : 'No submitted accounts'}>{query || status ? 'Try another email or submission state.' : 'Accounts will appear here after users submit them.'}</Empty> :
        <div className='admin-table-wrap'><table className='admin-table admin-accounts-table'>
          <caption className='sr-only'>Accounts across all users</caption>
          <thead><tr><th scope='col'>Account</th><th scope='col'>User</th><th scope='col'>Latest submit status</th><th scope='col'>Account status</th><th scope='col'>Last submitted</th></tr></thead>
          <tbody>{list.items.map(account => {
            const current = account.current_submission;
            return <tr key={account.id} onClick={() => onOpen(account.id)}>
              <td className='admin-primary-cell' data-label='Account'><button className='account-open' onClick={event => { event.stopPropagation(); onOpen(account.id); }}>{account.account}<ArrowRight aria-hidden='true' /></button>
                <span className='admin-cell-note'>{account.plan} · {account.vendor_name}</span>
                <span className='admin-cell-note'>{account.submission_count} {account.submission_count === 1 ? 'submission' : 'submissions'}{current?.remote_id ? ` · Remote ID ${current.remote_id}` : ''}</span>
                {current?.platform_account_id && <span className='admin-cell-note'>Platform ID {current.platform_account_id}</span>}
              </td>
              <td data-label='User'><span className='admin-email'>{account.user.email}</span></td>
              <td data-label='Latest submit status'><StateBadge value={account.latest_submission.status} /><span className='admin-cell-note'>Submission #{account.latest_submission.id}</span></td>
              <td data-label='Account status'><CurrentAccountStatus current={current} latest={account.latest_submission} refresh={current ? refreshes[current.id] : undefined} /></td>
              <td data-label='Last submitted'><time dateTime={account.latest_submission.created_at}>{dateTime(account.latest_submission.created_at)}</time></td>
            </tr>;
          })}</tbody>
        </table></div>}
      <Pagination data={list} noun='account' busy={loading} onPage={filters.setPage} />
    </section> : error ? <button className='button secondary' onClick={filters.refresh}>Try again</button> : null}
  </div>;
}

export function AdminHistory({ onOpen }: { onOpen: (id: string) => void }) {
  const filters = useListFilters();
  const { page, query, status, attempt, forceNext } = filters;
  const [list, setList] = useState<Page<AdminSubmission> | null>(null);
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [error, setError] = useState('');
  const [refreshes, setRefreshes] = useState<Record<string, StatusRefresh>>({});
  useEffect(() => {
    const abort = new AbortController();
    const force = forceNext.current; forceNext.current = false;
    setLoading(true); setError(''); setRefreshes({}); setSyncing(false);
    const params = new URLSearchParams({ page: String(page), search: query, status });
    request<Page<AdminSubmission>>(`/admin/submissions?${params}`, { signal: abort.signal })
      .then(async data => {
        if (abort.signal.aborted) return;
        setList(data); setLoading(false); setSyncing(true);
        await syncCurrentStatuses(data.items.map(item => item.current_submission), abort.signal, force,
          (id, state) => setRefreshes(current => abort.signal.aborted ? current : { ...current, [id]: state }),
          (id, submission) => setList(current => !current || abort.signal.aborted ? current : updateHistory(current, id, submission)),
        );
      })
      .catch(cause => { if (!abort.signal.aborted) setError(errorMessage(cause)); })
      .finally(() => { if (!abort.signal.aborted) { setLoading(false); setSyncing(false); } });
    return () => abort.abort();
  }, [page, query, status, attempt, forceNext]);
  return <div className='admin-page'>
    <div className='title-row'><div><p className='eyebrow admin-breadcrumb'>Account pool</p><h1>Submission history</h1><p className='subtitle'>Every submission across all accounts and users.</p></div><AdminBadge /></div>
    <ListToolbar id='admin-history' search={filters.search} onSearch={filters.setSearch} status={status} onStatus={filters.changeStatus} busy={loading || syncing} onRefresh={filters.refresh} />
    <ErrorNotice>{error}</ErrorNotice>
    {loading && !list ? <Loading label='Loading submission history…' /> : list ? <section className='panel admin-table-panel' aria-busy={loading}>
      <SubmissionTable data={list} refreshes={refreshes} onOpen={onOpen} />
      <Pagination data={list} noun='submission' busy={loading} onPage={filters.setPage} />
    </section> : error ? <button className='button secondary' onClick={filters.refresh}>Try again</button> : null}
  </div>;
}

export function PoolAccountDetails({ id, onBack, onSubmission }: { id: string; onBack: () => void; onSubmission: (id: string) => void }) {
  const filters = useListFilters();
  const { page, attempt, forceNext } = filters;
  const [detail, setDetail] = useState<AccountDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [error, setError] = useState('');
  const [refreshes, setRefreshes] = useState<Record<string, StatusRefresh>>({});
  useEffect(() => {
    const abort = new AbortController();
    const force = forceNext.current; forceNext.current = false;
    setLoading(true); setError(''); setRefreshes({}); setSyncing(false);
    request<AccountDetail>(`/admin/accounts/${encodeURIComponent(id)}?page=${page}`, { signal: abort.signal })
      .then(async data => {
        if (abort.signal.aborted) return;
        setDetail(data); setLoading(false); setSyncing(true);
        await syncCurrentStatuses([data.account.current_submission], abort.signal, force,
          (submissionID, state) => setRefreshes(current => abort.signal.aborted ? current : { ...current, [submissionID]: state }),
          (submissionID, submission) => setDetail(current => !current || abort.signal.aborted ? current : {
            account: updateAccount(current.account, submissionID, submission),
            history: updateHistory(current.history, submissionID, submission),
          }),
        );
      })
      .catch(cause => { if (!abort.signal.aborted) setError(errorMessage(cause)); })
      .finally(() => { if (!abort.signal.aborted) { setLoading(false); setSyncing(false); } });
    return () => abort.abort();
  }, [id, page, attempt, forceNext]);
  const account = detail?.account;
  const current = account?.current_submission;
  return <div className='admin-page'>
    <Back onClick={onBack} label='Accounts' />
    <ErrorNotice>{error}</ErrorNotice>
    {loading && !detail ? <Loading label='Loading account…' /> : detail && account ? <>
      <div className='title-row'><div><p className='eyebrow admin-breadcrumb'>Account pool</p><h1 className='admin-account-heading'>{account.account}</h1><p className='subtitle'>{account.plan} · {account.vendor_name}</p></div><AdminBadge /></div>
      <section className='panel admin-account-summary' aria-label='Account summary'>
        <dl className='admin-summary-grid'>
          <div><dt>User</dt><dd className='admin-email'>{account.user.email}</dd></div>
          <div><dt>Latest submit status</dt><dd><StateBadge value={account.latest_submission.status} /><span className='admin-cell-note'>Submission #{account.latest_submission.id}</span></dd></div>
          <div><dt>Current account status</dt><dd><CurrentAccountStatus current={current || null} latest={account.latest_submission} refresh={current ? refreshes[current.id] : undefined} /></dd></div>
          <div><dt>Remote account ID</dt><dd>{current?.remote_id || 'Not returned'}{current?.platform_account_id && <span className='admin-cell-note'>Platform ID {current.platform_account_id}</span>}</dd></div>
        </dl>
        <div className='admin-summary-footer'><span className='muted small'>{account.submission_count} {account.submission_count === 1 ? 'submission' : 'submissions'} · Last submitted {dateTime(account.latest_submission.created_at)}</span>
          {current && <button className='button secondary' onClick={() => onSubmission(current.id)}>Account details & bills<ArrowRight aria-hidden='true' /></button>}
        </div>
      </section>
      <div className='section-heading admin-history-heading'><div><h2>Submission history</h2><p className='muted small'>All submissions for this account and owner.</p></div>
        <button className='button secondary' disabled={loading || syncing} onClick={filters.refresh}><RefreshCw className={loading || syncing ? 'spin' : ''} aria-hidden='true' />Refresh</button>
      </div>
      <section className='panel admin-table-panel' aria-busy={loading}><SubmissionTable data={detail.history} refreshes={refreshes} onOpen={onSubmission} showOwner={false} /><Pagination data={detail.history} noun='submission' busy={loading} onPage={filters.setPage} /></section>
    </> : error ? <button className='button secondary' onClick={filters.refresh}>Try again</button> : null}
  </div>;
}
