import {
  ArrowRight,
  ChevronLeft,
  ChevronRight,
  Plus,
  RefreshCw,
  Search,
} from 'lucide-react';
import { useEffect, useState } from 'react';
import { errorMessage, request } from '../api';
import { ErrorNotice, Loading } from '../ui';
import { dateTime, Empty, StateBadge } from './shared';
import Select from './Select';
import type { SubmissionList } from './types';

export default function SubmissionHistory({
  onOpen,
  onSubmit,
}: {
  onOpen: (id: string) => void;
  onSubmit: () => void;
}) {
  const [list, setList] = useState<SubmissionList | null>(null);
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState('');
  const [page, setPage] = useState(1);
  const [attempt, setAttempt] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
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
    });
    request<SubmissionList>(`/submissions?${params}`, { signal: abort.signal })
      .then((data) => {
        if (!abort.signal.aborted) setList(data);
      })
      .catch((cause) => {
        if (!abort.signal.aborted) setError(errorMessage(cause));
      })
      .finally(() => {
        if (!abort.signal.aborted) {
          setLoading(false);
        }
      });
    return () => abort.abort();
  }, [page, query, status, attempt]);
  return (
    <div className='history-page'>
      <div className='title-row'>
        <div>
          <h1>Submission history</h1>
          <p className='subtitle'>
            Your account submissions and their results.
          </p>
        </div>
        <button className='button secondary' onClick={onSubmit}>
          <Plus aria-hidden='true' />
          Submit account
        </button>
      </div>
      <div className='history-toolbar'>
        <div className='search-field'>
          <Search aria-hidden='true' />
          <label className='sr-only' htmlFor='history-search'>
            Search accounts
          </label>
          <input
            id='history-search'
            type='search'
            placeholder='Search account or alias'
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        <div className='history-filter'>
          <Select
            id='history-status'
            label='Submission status'
            hiddenLabel
            value={status}
            onChange={(value) => {
              setStatus(value);
              setPage(1);
            }}
            options={[
              { value: '', label: 'All submission states' },
              ...['accepted', 'running', 'failed', 'unknown'].map((value) => ({
                value,
                label: value[0].toUpperCase() + value.slice(1),
              })),
            ]}
          />
        </div>
        <button
          className='button secondary icon-button'
          aria-label='Refresh submission history'
          title='Refresh'
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
        <Loading label='Loading submissions…' />
      ) : error && !list ? (
        <button
          className='button secondary'
          onClick={refreshHistory}
        >
          Try again
        </button>
      ) : (
        list && (
          <div className='panel history-panel' aria-busy={loading}>
            {!list.items.length ? (
              <Empty
                title={
                  query || status
                    ? 'No matching submissions'
                    : 'No accounts submitted yet'
                }
              >
                {query || status
                  ? 'Try another search or submission state.'
                  : 'Your account submissions will appear here.'}
              </Empty>
            ) : (
              <table className='history-table'>
                <thead>
                  <tr>
                    <th>Account / plan</th>
                    <th className='account-id-heading'>Account ID</th>
                    <th>Submission</th>
                    <th>Submitted</th>
                    <th>
                      <span className='sr-only'>Open</span>
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
                          {item.remote_alias || `Submission ${item.id}`} ·{' '}
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
                      <td data-label='Account ID' className='account-id-cell'>
                        {item.remote_id ? (
                          <span className='remote-account-id'>{item.remote_id}</span>
                        ) : (
                          <span className='muted'>Not returned</span>
                        )}
                      </td>
                      <td data-label='Submission'>
                        <StateBadge value={item.status} />
                      </td>
                      <td data-label='Submitted' className='date-cell'>
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
                {list.total} {list.total === 1 ? 'submission' : 'submissions'} ·
                Page {list.page} of{' '}
                {Math.max(1, Math.ceil(list.total / list.page_size))}
              </span>
              <div>
                <button
                  className='button secondary'
                  disabled={page <= 1}
                  onClick={() => setPage((n) => n - 1)}
                >
                  <ChevronLeft aria-hidden='true' />
                  <span>Previous</span>
                </button>
                <button
                  className='button secondary'
                  disabled={page * list.page_size >= list.total}
                  onClick={() => setPage((n) => n + 1)}
                >
                  <span>Next</span>
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
