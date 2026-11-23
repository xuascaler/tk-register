import { currentLanguage, t, translateMessage } from '../i18n';
import { ArrowLeft, Inbox, RefreshCw } from 'lucide-react';
import type { ReactNode } from 'react';
import type { Submission } from './types';
import { statusLabel, type StatusRefresh } from './status';

export function dateTime(value: string | null | undefined) {
  if (!value) return '—';
  const date = new Date(value);
  return Number.isNaN(date.valueOf())
    ? value
    : date.toLocaleString(currentLanguage(), {
        year: 'numeric',
        month: 'short',
        day: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
      });
}
export function humanize(value: string | null | undefined) {
  if (value === 'sync_failed') return t('Upstream sync failed');
  if (value === 're_login_failed' || value === 'relogin_failed') return t('Re-login failed');
  if (value === 'auth_json') return 'Auth JSON';
  return value
    ? t(value.replaceAll('_', ' ').replace(/^./, (s) => s.toUpperCase()))
    : t('Not checked');
}
export function StateBadge({ value }: { value: string | null }) {
  return (
    <span className={`state-badge state-${value || 'unchecked'}`}>
      {humanize(value)}
    </span>
  );
}
export function AccountStatus({
  submission,
  refresh,
}: {
  submission: Submission;
  refresh?: StatusRefresh;
}) {
  const label = t(statusLabel(submission, refresh));
  return (
    <div className='account-status' aria-live='polite'>
      {submission.status === 'accepted' && submission.remote_status ? (
        <StateBadge value={submission.remote_status} />
      ) : (
        <span className='state-badge state-unchecked'>{label}</span>
      )}
      {refresh?.checking ? (
        <span className='account-status-note'>
          <RefreshCw className='spin' aria-hidden='true' /> {t('Checking…')}
        </span>
      ) : refresh?.error ? (
        <span className='account-status-note status-update-error'>
          {t('Update failed.')} {translateMessage(refresh.error)}
        </span>
      ) : null}
      {submission.status_checked_at && (
        <time
          className='account-status-note'
          dateTime={submission.status_checked_at}
        >
          {t('Updated')} {dateTime(submission.status_checked_at)}
        </time>
      )}
    </div>
  );
}
export function Empty({
  title,
  children,
}: {
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className='empty-state'>
      <span className='empty-icon'>
        <Inbox aria-hidden='true' />
      </span>
      <h2>{title}</h2>
      {children && <div className='muted'>{children}</div>}
    </div>
  );
}
export function Back({ onClick, label = t('Submission history') }: { onClick: () => void; label?: string }) {
  return (
    <button className='back-link' type='button' onClick={onClick}>
      <ArrowLeft aria-hidden='true' />
      {label}
    </button>
  );
}
export function number(value: number | null | undefined) {
  return value == null ? '—' : value.toLocaleString(currentLanguage());
}
