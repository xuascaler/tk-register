import { t, translateMessage } from '../i18n';
import { Check, Copy, RefreshCw } from 'lucide-react';
import {
  useEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from 'react';
import { errorMessage, request } from '../api';
import { ErrorNotice, Loading } from '../ui';
import type { SubmissionDetail as Detail, Vendor } from './types';
import {
  AccountStatus,
  Back,
  dateTime,
  humanize,
  StateBadge,
} from './shared';
import { canCheckStatus, executionError, type StatusRefresh } from './status';
import type { AdminDetail } from '../admin/types';
import { CurrentAccountStatus } from '../admin/shared';

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className='fact'>
      <dt>{label}</dt>
      <dd>{children ?? '—'}</dd>
    </div>
  );
}
function display(value: unknown): string {
  if (value === null || value === undefined || value === '')
    return t('Not provided');
  return Array.isArray(value) ? value.map(String).join(', ') : String(value);
}
function publicParameters(parameters: Record<string, unknown>) {
  const safe: Record<string, unknown> = {};
  for (const key of [
    'account',
    'plan',
    'register_location',
    'register_time',
    'plan_start_time',
    'plan_end_time',
    'payment_method',
    'register_device',
    'balance',
    'recharge_time',
    'warranty_period',
    'tags',
    'remark',
  ])
    if (key in parameters) safe[key] = parameters[key];
  safe.password = '[hidden]';
  safe.auth_json = '[hidden]';
  return safe;
}

export default function SubmissionDetails({
  id,
  onBack,
  endpointPrefix = '/submissions',
  backLabel,
}: {
  id: string;
  onBack: () => void;
  endpointPrefix?: '/submissions' | '/admin/submissions';
  backLabel?: string;
}) {
  const [detail, setDetail] = useState<(Detail & Partial<AdminDetail>) | null>(null);
  const [vendor, setVendor] = useState<Vendor | null>(null);
  const [tab, setTab] = useState<'overview' | 'parameters'>(
    'overview',
  );
  const [error, setError] = useState('');
  const [currentRefresh, setCurrentRefresh] = useState<StatusRefresh>({});
  const [busy, setBusy] = useState<'load' | 'current-status' | ''>('load');
  const [attempt, setAttempt] = useState(0);
  const [json, setJSON] = useState(false);
  const [copied, setCopied] = useState(false);
  const manualRequest = useRef<AbortController | null>(null);
  const tabs = ['overview', 'parameters'] as const;
  function changeTab(value: (typeof tabs)[number]) {
    setTab(value);
    setError('');
  }
  function tabKey(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const next =
      event.key === 'ArrowRight'
        ? (index + 1) % tabs.length
        : event.key === 'ArrowLeft'
          ? (index + tabs.length - 1) % tabs.length
          : event.key === 'Home'
            ? 0
            : event.key === 'End'
              ? tabs.length - 1
              : -1;
    if (next < 0) return;
    event.preventDefault();
    changeTab(tabs[next]);
    document.getElementById(`detail-tab-${tabs[next]}`)?.focus();
  }
  useEffect(() => {
    const abort = new AbortController();
    setBusy('load');
    setError('');
    setCurrentRefresh({});
    setDetail(null);
    Promise.all([
      request<Detail & Partial<AdminDetail>>(`${endpointPrefix}/${encodeURIComponent(id)}`, {
        signal: abort.signal,
      }),
      request<{ vendors: Vendor[] }>('/vendors', { signal: abort.signal }),
    ])
      .then(([data, vendors]) => {
        if (abort.signal.aborted) return;
        setDetail(data);
        const currentVendor =
          vendors.vendors.find((v) => v.id === data.submission.vendor_id) ||
          null;
        setVendor(currentVendor);
        // Successful submissions schedule their own bounded background syncs.
        // Opening saved history must not add an immediate supplier query.
      })
      .catch((cause) => {
        if (!abort.signal.aborted) setError(errorMessage(cause));
      })
      .finally(() => {
        if (!abort.signal.aborted) setBusy('');
      });
    return () => {
      abort.abort();
      manualRequest.current?.abort();
    };
  }, [id, attempt, endpointPrefix]);
  async function refreshCurrentStatus() {
    const target = detail?.current_submission;
    if (!detail || busy || !target || !canCheckStatus(target)) return;
    const abort = new AbortController();
    manualRequest.current = abort;
    setBusy('current-status');
    setCurrentRefresh({ checking: true });
    try {
      const result = await request<Detail & Partial<AdminDetail>>(
        `${endpointPrefix}/${encodeURIComponent(target.id)}/status`,
        { method: 'POST', body: { force: true }, signal: abort.signal },
      );
      if (abort.signal.aborted) return;
      setDetail(current => !current ? current : target.id === current.submission.id ? result : {
        ...current, current_submission: result.current_submission || result.submission,
      });
      const failure = executionError(result);
      setCurrentRefresh({ error: failure });
    } catch (cause) {
      if (!abort.signal.aborted) {
        setCurrentRefresh({ error: errorMessage(cause) });
      }
    } finally {
      if (!abort.signal.aborted) setBusy('');
    }
  }
  async function copyID() {
    try {
      await navigator.clipboard.writeText(detail?.submission.remote_id || '');
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      setError('Unable to copy. You can select and copy the account ID.');
    }
  }
  const submission = detail?.submission;
  return (
    <div className='detail-page'>
      <Back onClick={onBack} label={backLabel} />
      {busy === 'load' ? (
        <Loading label={t('Loading account details…')} />
      ) : !detail || !submission ? (
        <>
          <ErrorNotice>{error}</ErrorNotice>
          <button
            className='button secondary'
            onClick={() => setAttempt((n) => n + 1)}
          >
            {t('Try again')}
          </button>
        </>
      ) : (
        <>
          <div className='detail-heading'>
            <div>
              <p className='eyebrow'>{t('Submitted account')}</p>
              <h1>{submission.account}</h1>
              <p className='subtitle'>{submission.plan}</p>
            </div>
            {endpointPrefix === '/admin/submissions' ? <div className='admin-detail-status'>
              <span className='admin-cell-note'>{t('Current account')}{detail.current_submission ? ` · ${t('Submission #{{id}}', { id: detail.current_submission.id })}` : ''}</span>
              <CurrentAccountStatus current={detail.current_submission || null} latest={submission} refresh={currentRefresh} />
              {submission.can_manage !== false && detail.current_submission && <button className='button secondary' disabled={Boolean(busy) || !vendor?.enabled || !vendor.configured} onClick={() => void refreshCurrentStatus()}>
                <RefreshCw className={currentRefresh.checking ? 'spin' : ''} aria-hidden='true' />{currentRefresh.checking ? t('Checking…') : t('Refresh account status')}
              </button>}
            </div> : <AccountStatus
              submission={submission}
            />}
          </div>
          <div className='detail-summary'>
            <span>{submission.vendor_name}</span>
            {submission.account_source && <span>{t('Source')} {submission.account_source.label === 'myself' ? t('myself') : submission.account_source.label}</span>}
            {detail.user && <span className='admin-email'>{t('Submitted by')} {detail.user.email}</span>}
            {endpointPrefix === '/admin/submissions' && <span>{t('Submission #{{id}}', { id: submission.id })}</span>}
            <span>{t('Submitted')} {dateTime(submission.created_at)}</span>
            <span>{submission.submit_mode === 'restore_update' ? t('Restore by update') : submission.submit_mode === 'create_fallback' ? t('Create after upstream lookup') : t('Create account')}</span>
            <span>
              {t('Submission')} <StateBadge value={submission.status} />
            </span>
          </div>
          {submission.status === 'unknown' && (
            <div className='notice'>
              {submission.submit_mode === 'restore_update' || submission.submit_mode === 'create_fallback'
                ? t('The supplier result is unconfirmed. Do not resubmit. Contact an administrator to verify the previous attempt.')
                : t('The platform result is unconfirmed. You may submit this account again; each attempt is saved separately. The earlier attempt may already have created the account.')}
            </div>
          )}
          {submission.status === 'running' && (
            <div className='notice'>
              {t('This submission is being processed.')}{' '}
              <button
                className='text-link'
                onClick={() => setAttempt((n) => n + 1)}
              >
                {t('Refresh details')}
              </button>{' '}
              {t('to check the latest result.')}
            </div>
          )}
          {submission.status === 'failed' && (
            <div className='notice error'>
              {t('The platform did not accept this submission. See the request history below for details.')}
            </div>
          )}
          <div
            className='detail-tabs'
            role='tablist'
            aria-label={t('Account information')}
          >
            {tabs.map((value, index) => (
              <button
                id={`detail-tab-${value}`}
                role='tab'
                aria-controls={`detail-panel-${value}`}
                aria-selected={tab === value}
                tabIndex={tab === value ? 0 : -1}
                className={tab === value ? 'selected' : ''}
                onKeyDown={(event) => tabKey(event, index)}
                onClick={() => changeTab(value)}
                key={value}
              >
                {value === 'parameters'
                  ? t('Submitted parameters')
                  : humanize(value)}
              </button>
            ))}
          </div>
          <ErrorNotice>{error}</ErrorNotice>
          <div
            role='tabpanel'
            id={`detail-panel-${tab}`}
            aria-labelledby={`detail-tab-${tab}`}
          >
            {tab === 'overview' && (
              <>
                <section className='panel details-section'>
                  <div className='section-heading'>
                    <h2>{t('Account information')}</h2>
                  </div>
                  <dl className='facts-grid'>
                    <Fact label={t('Account')}>{submission.account}</Fact>
                    <Fact label={t('Platform account ID')}>
                      {submission.platform_account_id || t('Not returned')}
                    </Fact>
                    <Fact label={t('Remote account ID')}>
                      {submission.remote_id ? (
                        <span className='inline-copy'>
                          {submission.remote_id}
                          <button
                            className='copy-button'
                            aria-label={t('Copy remote account ID')}
                            onClick={copyID}
                          >
                            {copied ? <Check /> : <Copy />}
                          </button>
                        </span>
                      ) : (
                        t('Not assigned')
                      )}
                    </Fact>
                    <Fact label={t('Plan')}>{submission.plan}</Fact>
                    <Fact label={t('Registration date')}>
                      {display(submission.parameters.register_time)}
                    </Fact>
                    <Fact label={t('Registration location')}>
                      {display(submission.parameters.register_location)}
                    </Fact>
                    <Fact label={t('Plan start date')}>
                      {display(submission.parameters.plan_start_time)}
                    </Fact>
                    <Fact label={t('Plan end date')}>
                      {display(submission.parameters.plan_end_time)}
                    </Fact>
                    <Fact label={t('Registration device')}>
                      {display(submission.parameters.register_device)}
                    </Fact>
                    <Fact label={t('Recharge date')}>
                      {display(submission.parameters.recharge_time)}
                    </Fact>
                    <Fact label={t('Password')}>{t('Hidden')}</Fact>
                    <Fact label='Auth JSON'>{t('Hidden')}</Fact>
                  </dl>
                </section>
              </>
            )}
            {tab === 'parameters' && (
              <section className='panel details-section'>
                <div className='section-heading'>
                  <div>
                    <h2>{t('Submitted parameters')}</h2>
                    <p className='field-help'>
                      {t('A snapshot of the original submission. Credentials stay hidden.')}
                    </p>
                  </div>
                  <button
                    className='button secondary compact'
                    aria-pressed={json}
                    onClick={() => setJSON(!json)}
                  >
                    {json ? t('Show fields') : t('View JSON')}
                  </button>
                </div>
                {json ? (
                  <pre className='parameter-json'>
                    {JSON.stringify(
                      publicParameters(submission.parameters),
                      null,
                      2,
                    )}
                  </pre>
                ) : (
                  <dl className='facts-grid'>
                    {Object.entries(
                      publicParameters(submission.parameters),
                    ).map(([key, value]) => (
                      <Fact label={humanize(key)} key={key}>
                        {key === 'password' || key === 'auth_json' ? t('[hidden]') : display(value)}
                      </Fact>
                    ))}
                  </dl>
                )}
              </section>
            )}
          </div>
          <section className='panel execution-section'>
            <div className='section-heading'>
              <h2>{t('Request history')}</h2>
              <span className='small muted'>{t('Submission')} {submission.id}</span>
            </div>
            {detail.executions.length ? (
              <ul className='execution-list'>
                {detail.executions.map((execution) => (
                  <li key={execution.id}>
                    <div>
                      <strong>
                        {execution.protocol_name}{' '}
                        <span className='muted small'>
                          v{execution.protocol_version}
                        </span>
                      </strong>
                      <StateBadge value={execution.status} />
                    </div>
                    <p className='small muted'>
                      {dateTime(execution.created_at)}{' '}
                      {execution.http_status !== null &&
                        `· HTTP ${execution.http_status}`}
                    </p>
                    {execution.message && (
                      <p className='execution-message'>{translateMessage(execution.message)}</p>
                    )}
                    <details>
                      <summary>{t('Request details')}</summary>
                      <dl>
                        <Fact label={t('Execution ID')}>{execution.id}</Fact>
                        <Fact label={t('Request ID')}>{execution.request_id}</Fact>
                        {execution.error_code && (
                          <Fact label={t('Error code')}>{execution.error_code}</Fact>
                        )}
                      </dl>
                      {execution.error_body && <div className='execution-error-body'><strong>{t('Upstream error')}</strong><pre>{execution.error_body}</pre></div>}
                      {(execution.error_body_truncated || execution.error_body_incomplete) && <p className='muted small'>{t('The error response was truncated or incomplete.')}</p>}
                      {execution.error_body_omitted_reason && !execution.error_body && <p className='muted small'>{t('No safe error details are available for display.')}</p>}
                    </details>
                  </li>
                ))}
              </ul>
            ) : (
              <p className='muted'>{t('No requests recorded yet.')}</p>
            )}
          </section>
        </>
      )}
    </div>
  );
}
