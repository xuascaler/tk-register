import { t } from '../i18n';
import {
  ArrowRight,
  ChevronLeft,
  ChevronRight,
  Plus,
  RefreshCw,
  Search,
} from 'lucide-react';
import { useEffect, useState } from 'react';
import { displayError, type DisplayError, request } from '../api';
import { ErrorNotice, Loading } from '../ui';
import { dateTime, Empty, StateBadge } from './shared';
import Select from './Select';
import SourceFilter from '../members/SourceFilter';
import { useMemberScope } from '../members/MemberScope';
import type { SubmissionList } from './types';

export default function SubmissionHistory({
  onOpen,
  onSubmit,
}: {
  onOpen: (id: string) => void;
  onSubmit: () => void;
}) {
  const { selection: sources } = useMemberScope();
  const [list, setList] = useState<SubmissionList | null>(null);
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState('');
  const [page, setPage] = useState(1);
  const [attempt, setAttempt] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<DisplayError>('');
  function refreshHistory() {
    setAttempt((n) => n + 1);
  }
  useEffect(() => {
    const timer = setTimeout(() => {
      setQuery(search.trim());
      setPage(1);
    }, 300);
    return () => clearTimeout(timer);
  }, [search]);
  useEffect(() => {
    const abort = new AbortController();
    setLoading(true);
    setError('');
    const params = new URLSearchParams({
      page: String(page),
      search: query,
      status,
      sources,
    });
    request<SubmissionList>(`/submissions?${params}`, { signal: abort.signal })
      .then((data) => {
        if (!abort.signal.aborted) setList(data);
      })
      .catch((cause) => {
        if (!abort.signal.aborted) setError(displayError(cause));
      })
      .finally(() => {
        if (!abort.signal.aborted) {
          setLoading(false);
        }
      });
    return () => abort.abort();
  }, [page, query, status, attempt, sources]);
  return (
    <div className='history-page'>
      <div className='title-row'>
        <div>
          <h1>{t('Submission history')}</h1>
          <p className='subtitle'>
            {t('Your account submissions and their results.')}
          </p>
        </div>
        <button className='button secondary' onClick={onSubmit}>
          <Plus aria-hidden='true' />
          {t('Submit account')}
        </button>
      </div>
      <div className='history-toolbar member-history-toolbar'>
        <div className='search-field'>
          <Search aria-hidden='true' />
          <label className='sr-only' htmlFor='history-search'>
            {t('Search accounts')}
          </label>
          <input
            id='history-search'
            type='search'
            placeholder={t('Search account or alias')}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        <SourceFilter id='history-source' onChange={() => setPage(1)} />
        <div className='history-filter'>
          <Select
            id='history-status'
            label={t('Submission status')}
            hiddenLabel={false}
            value={status}
            onChange={(value) => {
              setStatus(value);
              setPage(1);
            }}
            options={[
              { value: '', label: t('All submission states') },
              ...['accepted', 'running', 'failed', 'unknown'].map((value) => ({
                value,
                label: t(value[0].toUpperCase() + value.slice(1)),
              })),
            ]}
          />
        </div>
        <button
          className='button secondary icon-button'
          aria-label={t('Refresh submission history')}
          title={t('Refresh')}
          disabled={loading}
          onClick={refreshHistory}
        >
          <RefreshCw
            className={loading ? 'spin' : ''}
            aria-hidden='true'
          />
        </button>
      </div>
      <ErrorNotice>{error}</ErrorNotice>
      {loading && !list ? (
        <Loading label={t('Loading submissions…')} />
      ) : error && !list ? (
        <button
          className='button secondary'
          onClick={refreshHistory}
        >
          {t('Try again')}
        </button>
      ) : (
        list && (
          <div className='panel history-panel' aria-busy={loading}>
            {!list.items.length ? (
              <Empty
                title={
                  query || status
                    ? t('No matching submissions')
                    : t('No accounts submitted yet')
                }
              >
                {query || status
                  ? t('Try another search or submission state.')
                  : t('Your account submissions will appear here.')}
              </Empty>
            ) : (
              <table className='history-table member-history-table'>
                <thead>
                  <tr>
                    <th>{t('Account / plan')}</th>
                    <th>{t('Source')}</th>
                    <th className='account-id-heading'>{t('Account ID')}</th>
                    <th>{t('Submission')}</th>
                    <th>{t('Submitted')}</th>
                    <th>
                      <span className='sr-only'>{t('Open')}</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {list.items.map((item) => (
                    <tr key={item.id} onClick={() => onOpen(item.id)}>
                      <td className='history-account'>
                        <button
                          className='account-open'
                          onClick={(e) => {
                            e.stopPropagation();
                            onOpen(item.id);
                          }}
                        >
                          {item.account}
                          <ArrowRight
                            className='mobile-arrow'
                            aria-hidden='true'
                          />
                        </button>
                        <span>{item.plan}</span>
                        <span className='row-meta'>
                          {item.remote_alias?.replaceAll('温州润晨', 'xxxxx') || t('Submission {{id}}', { id: item.id })} ·{' '}
                          {item.vendor_name}
                        </span>
                        <span className='row-meta'>
                          {[
                            item.parameters.register_location,
                            item.parameters.register_device,
                          ]
                            .filter((v) => typeof v === 'string')
                            .join(' · ')}
                        </span>
                      </td>
                      <td data-label={t('Source')} className='member-history-source'>{item.account_source?.label && item.account_source.label !== 'myself' ? item.account_source.label : t('myself')}</td>
                      <td data-label={t('Account ID')} className='account-id-cell'>
                        {item.remote_id ? (
                          <span className='remote-account-id'>{item.remote_id}</span>
                        ) : (
                          <span className='muted'>{t('Not returned')}</span>
                        )}
                      </td>
                      <td data-label={t('Submission')}>
                        <StateBadge value={item.status} />
                      </td>
                      <td data-label={t('Submitted')} className='date-cell'>
                        <time dateTime={item.created_at}>
                          {dateTime(item.created_at)}
                        </time>
                      </td>
                      <td className='row-chevron'>
                        <ChevronRight aria-hidden='true' />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
            <div className='pagination'>
              <span className='muted small'>
                {t(list.total === 1
                  ? '{{count}} submission · Page {{page}} of {{pages}}'
                  : '{{count}} submissions · Page {{page}} of {{pages}}',
                  { count: list.total, page: list.page, pages: Math.max(1, Math.ceil(list.total / list.page_size)) })}
              </span>
              <div>
                <button
                  className='button secondary'
                  disabled={page <= 1}
                  onClick={() => setPage((n) => n - 1)}
                >
                  <ChevronLeft aria-hidden='true' />
                  <span>{t('Previous')}</span>
                </button>
                <button
                  className='button secondary'
                  disabled={page * list.page_size >= list.total}
                  onClick={() => setPage((n) => n + 1)}
                >
                  <span>{t('Next')}</span>
                  <ChevronRight aria-hidden='true' />
                </button>
              </div>
            </div>
          </div>
        )
      )}
    </div>
  );
}
