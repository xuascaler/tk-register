import { t } from '../i18n';
import { ArrowRight, ChevronLeft, ChevronRight, LockKeyhole, RefreshCw, Search } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { AccountStatus, dateTime, Empty, StateBadge } from '../submissions/shared';
import Select from '../submissions/Select';
import type { StatusRefresh } from '../submissions/status';
import type { Submission } from '../submissions/types';
import type { AdminSubmission, Page } from './types';
export { syncCurrentStatuses } from './status';

export function AdminBadge() {
  return <span className='badge'><LockKeyhole aria-hidden='true' />{t("Admin only")}</span>;
}

export function Pagination({ data, noun, busy = false, onPage }: {
  data: Pick<Page<unknown>, 'page' | 'page_size' | 'total'>;
  noun: string;
  busy?: boolean;
  onPage: (page: number) => void;
}) {
  return <div className='pagination'>
    <span className='muted small' role='status'>{t('{{total}} {{noun}} · Page {{page}} of {{pages}}', { total: data.total, noun: t(data.total === 1 ? noun : noun + 's'), page: data.page, pages: Math.max(1, Math.ceil(data.total / data.page_size)) })}</span>
    <div>
      <button className='button secondary' disabled={busy || data.page <= 1} onClick={() => onPage(data.page - 1)}><ChevronLeft aria-hidden='true' />{t("Previous")}</button>
      <button className='button secondary' disabled={busy || data.page * data.page_size >= data.total} onClick={() => onPage(data.page + 1)}>{t("Next")}<ChevronRight aria-hidden='true' /></button>
    </div>
  </div>;
}

export function ListToolbar({ id, search, onSearch, status, onStatus, busy, onRefresh, placeholder = 'Search account or user email' }: {
  id: string; search: string; onSearch: (value: string) => void;
  status?: string; onStatus?: (value: string) => void;
  busy: boolean; onRefresh: () => void; placeholder?: string;
}) {
  return <div className={`history-toolbar ${onStatus ? '' : 'admin-search-toolbar'}`}>
    <div className='search-field'><Search aria-hidden='true' />
      <label className='sr-only' htmlFor={`${id}-search`}>{t(placeholder)}</label>
      <input id={`${id}-search`} type='search' placeholder={t(placeholder)} value={search} onChange={event => onSearch(event.target.value)} />
    </div>
    {onStatus && <div className='history-filter'><Select id={`${id}-status`} label={t("Submission status")} hiddenLabel value={status || ''} onChange={onStatus} options={[
      { value: '', label: t('All submission states') },
      ...['accepted', 'running', 'failed', 'unknown'].map(value => ({ value, label: t(value[0].toUpperCase() + value.slice(1)) })),
    ]} /></div>}
    <button className='button secondary icon-button' title={t("Refresh")} aria-label={t('Refresh {{name}}', { name: id === 'users' ? t('Users').toLowerCase() : t(id.replaceAll('-', ' ')) })} disabled={busy} onClick={onRefresh}><RefreshCw className={busy ? 'spin' : ''} aria-hidden='true' /></button>
  </div>;
}

export function useListFilters() {
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState('');
  const [attempt, setAttempt] = useState(0);
  const forceNext = useRef(false);
  useEffect(() => {
    const timer = setTimeout(() => { setQuery(search.trim()); setPage(1); }, 300);
    return () => clearTimeout(timer);
  }, [search]);
  const refresh = () => { forceNext.current = true; setAttempt(value => value + 1); };
  const resetSearch = () => { setSearch(''); setQuery(''); setPage(1); };
  const changeStatus = (value: string) => { setStatus(value); setPage(1); };
  return { search, setSearch, resetSearch, query, page, setPage, status, changeStatus, attempt, forceNext, refresh };
}


export function CurrentAccountStatus({ current, latest, refresh }: { current: Submission | null; latest: Submission; refresh?: StatusRefresh }) {
  if (current) return <AccountStatus submission={current} refresh={refresh} />;
  if (latest.status === 'running' || latest.status === 'unknown') return <AccountStatus submission={latest} />;
  return <span className='state-badge state-unchecked'>{t("Not available")}</span>;
}

export function SubmissionTable({ data, onOpen, showOwner = true }: {
  data: Page<AdminSubmission>;
  onOpen: (id: string) => void; showOwner?: boolean;
}) {
  if (!data.items.length) return <Empty title={t("No matching submissions")}>{t("Try another search or submission state.")}</Empty>;
  return <div className='admin-table-wrap'><table className={`admin-table admin-submission-table ${showOwner ? '' : 'admin-submission-table-without-owner'}`}>
    <caption className='sr-only'>{t("Submission history")}</caption>
    <thead><tr><th scope='col'>{t("Account / submission")}</th>{showOwner && <th scope='col'>{t("Submitted by")}</th>}<th scope='col'>{t("Submit status")}</th><th scope='col'>{t("Submitted")}</th></tr></thead>
    <tbody>{data.items.map(item => <tr key={item.id} onClick={() => onOpen(item.id)}>
      <td className='admin-primary-cell' data-label={t("Account / submission")}><button className='account-open' onClick={event => { event.stopPropagation(); onOpen(item.id); }}>{item.account}<ArrowRight aria-hidden='true' /></button>
        <span className='admin-cell-note'>{item.plan} · {item.vendor_name}</span>
        <span className='admin-cell-note'>{t("Submission #")}{item.id}{item.remote_id ? t(' · Remote ID {{id}}', { id: item.remote_id }) : ''}</span>
      </td>
      {showOwner && <td data-label={t("Submitted by")}><span className='admin-email'>{item.user.email}</span></td>}
      <td data-label={t("Submit status")}><StateBadge value={item.status} /></td>
      <td data-label={t("Submitted")}><time dateTime={item.created_at}>{dateTime(item.created_at)}</time></td>
    </tr>)}</tbody>
  </table></div>;
}
