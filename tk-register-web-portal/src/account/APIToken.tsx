import { currentLanguage, t, translateMessage } from '../i18n';
import { Check, Copy, KeyRound, RefreshCw, ShieldCheck, Trash2, X } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { displayError, localizedError, type DisplayError, request } from '../api';
import { ErrorNotice, Loading } from '../ui';
import './api-token.css';

type Token = {
  id: string;
  revision: number;
  prefix: string;
  last_four: string;
  created_at: string;
  last_used_at: string | null;
  expires_at: string | null;
  revoked_at: string | null;
  scopes?: string[];
};
type TokenResponse = { token: Token | null; plaintext_token?: string };
type Action = 'generate' | 'rotate' | 'revoke';
const tokenPath = '/account/api-token';

function dateLabel(value: string) {
  return new Intl.DateTimeFormat(currentLanguage(), { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value));
}

function ConfirmTokenAction({ action, busy, error, onClose, onConfirm }: {
  action: 'rotate' | 'revoke'; busy: boolean; error: DisplayError;
  onClose: () => void; onConfirm: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  const revoking = action === 'revoke';
  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    const element = dialog.current;
    element?.showModal();
    cancel.current?.focus();
    return () => {
      element?.close();
      document.body.style.overflow = overflow;
      previous?.focus();
    };
  }, []);
  return <dialog ref={dialog} className='api-token-dialog' aria-labelledby='token-dialog-title' aria-describedby='token-dialog-description' onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}>
    <div className='api-token-dialog-heading'>
      <h2 id='token-dialog-title'>{revoking ? t('Revoke API token?') : t('Regenerate API token?')}</h2>
      <button type='button' className='api-token-icon-button' aria-label={t('Close dialog')} disabled={busy} onClick={onClose}><X aria-hidden='true' /></button>
    </div>
    <p id='token-dialog-description'>{revoking
      ? t('This token will stop working immediately. You can generate a new token when you need API access again.')
      : t('Your current token will stop working immediately. Copy the new token and update any scripts that use it.')}</p>
    <ErrorNotice>{error}</ErrorNotice>
    <div className='api-token-dialog-actions'>
      <button ref={cancel} type='button' className='button secondary' disabled={busy} onClick={onClose}>{t('Cancel')}</button>
      <button type='button' className={`button secondary ${revoking ? 'api-token-danger' : ''}`} disabled={busy} onClick={onConfirm}>
        {busy ? <RefreshCw className='spin' aria-hidden='true' /> : revoking ? <Trash2 aria-hidden='true' /> : <RefreshCw aria-hidden='true' />}
        {busy ? t('Saving…') : revoking ? t('Revoke token') : t('Regenerate')}
      </button>
    </div>
  </dialog>;
}

export default function APIToken() {
  const [data, setData] = useState<TokenResponse | null>(null);
  const [plaintext, setPlaintext] = useState('');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<DisplayError>('');
  const [notice, setNotice] = useState('');
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState('');
  const [confirmation, setConfirmation] = useState<'rotate' | 'revoke' | null>(null);
  const [attempt, setAttempt] = useState(0);
  const mounted = useRef(true);
  const submitting = useRef(false);
  const copyTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const tokenInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      clearTimeout(copyTimer.current);
    };
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError('');
    request<TokenResponse>(tokenPath, { signal: controller.signal })
      .then(result => { if (!controller.signal.aborted) setData({ token: result.token }); })
      .catch(cause => { if (!controller.signal.aborted) setError(displayError(cause)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [attempt]);

  const token = data?.token;
  const expired = Boolean(token?.expires_at && new Date(token.expires_at).getTime() <= Date.now());
  const active = Boolean(token && !token.revoked_at && !expired);
  const canUpdateAuthJSON = Boolean(token?.scopes?.includes('claude:auth-json:update'));

  async function copy(value: string) {
    setCopyError('');
    try {
      await navigator.clipboard.writeText(value);
      if (!mounted.current) return;
      setCopied(true);
      clearTimeout(copyTimer.current);
      copyTimer.current = setTimeout(() => setCopied(false), 2500);
    } catch {
      if (!mounted.current) return;
      tokenInput.current?.focus();
      tokenInput.current?.select();
      setCopyError('Clipboard access was unavailable. Select the text and copy it manually.');
    }
  }

  async function mutate(action: Action) {
    if (submitting.current || !data || ((action === 'rotate' || action === 'revoke') && !token)) return;
    submitting.current = true;
    setBusy(true);
    setError('');
    setNotice('');
    setCopyError('');
    setCopied(false);
    clearTimeout(copyTimer.current);
    try {
      const result = await request<TokenResponse>(action === 'rotate' ? `${tokenPath}/rotate` : tokenPath, {
        method: action === 'revoke' ? 'DELETE' : 'POST',
        body: action === 'generate' ? {} : { token_id: token!.id, revision: token!.revision },
      });
      if (!mounted.current) return;
      // Keep the one-time secret separate from metadata; neither is persisted by this component.
      setData({ token: result.token });
      setPlaintext(action === 'revoke' ? '' : result.plaintext_token || '');
      setConfirmation(null);
      if (action === 'revoke') setNotice('API token revoked.');
      else if (result.plaintext_token) setNotice(action === 'rotate' ? 'New token generated. Your previous token is now inactive.' : 'Token generated. Copy it now; it will not be shown again.');
      else setError('The token was created, but its value was not received. Regenerate it to get a new token.');
    } catch (cause) {
      if (!mounted.current) return;
      // A connection failure can happen after the server has already changed the token.
      // Read current metadata instead of automatically repeating a destructive mutation.
      setPlaintext('');
      setError(displayError(cause));
      setConfirmation(null);
      try {
        const current = await request<TokenResponse>(tokenPath);
        if (mounted.current) setData({ token: current.token });
      } catch {
        if (mounted.current) setData(null);
      }
    } finally {
      submitting.current = false;
      if (mounted.current) setBusy(false);
    }
  }

  function confirm(action: 'rotate' | 'revoke') {
    setError('');
    setConfirmation(action);
  }

  return <section className='account-section api-token-section' aria-labelledby='api-token-title'>
    <h2 id='api-token-title'>{t('API token')}</h2>
    <div className='account-row'>
      <div className='account-label'>{t('Personal access')}</div>
      <div className='account-value api-token-content' aria-busy={busy || loading}>
        <p className='api-token-description'>{t('Submit accounts, read saved status and daily usage, and update Auth JSON for your own accounts. API submissions appear in your submission history.')}</p>
        {loading ? <Loading label={t('Loading token…')} /> : data ? <>
          <div className={`api-token-card ${plaintext && active ? 'has-plaintext' : ''}`}>
            <div className='api-token-card-heading'>
              <span className='api-token-key'><KeyRound aria-hidden='true' /></span>
              <div><h3>{active ? t('Your API token') : token?.revoked_at ? t('Token revoked') : expired ? t('Token expired') : t('No API token yet')}</h3><p>{active ? canUpdateAuthJSON ? t('Submission, account lookup, daily usage, and Auth JSON updates') : t('Submission, account lookup, and daily usage access') : t('Generate a token to access your accounts through the API.')}</p></div>
              {active && <span className='verified'><ShieldCheck aria-hidden='true' />{t('Active')}</span>}
            </div>
            {active && token && <>
              <div className='api-token-value-row'>
                <input ref={tokenInput} className='api-token-value' aria-label={plaintext ? t('New API token') : t('Masked API token')} value={plaintext || `${token.prefix}••••••••••••${token.last_four}`} readOnly autoComplete='off' spellCheck={false} />
                {plaintext && <button type='button' className='api-token-icon-button' onClick={() => void copy(plaintext)} aria-label={copied ? t('Token copied') : t('Copy API token')} title={copied ? t('Copied') : t('Copy token')}>{copied ? <Check aria-hidden='true' /> : <Copy aria-hidden='true' />}</button>}
              </div>
              <p className='account-help api-token-secret-help'>{plaintext ? t('Copy and save this token securely. Leaving this page hides it.') : t('For security, only the beginning and end are shown. Regenerate if you need a new token.')}</p>
              {token.scopes && !canUpdateAuthJSON && <p className='account-help'>{t('Auth JSON update access is unavailable for this token. Ask an administrator to check its permissions.')}</p>}
              <dl className='api-token-metadata'><div><dt>{t('Created')}</dt><dd>{dateLabel(token.created_at)}</dd></div><div><dt>{t('Last used')}</dt><dd>{token.last_used_at ? dateLabel(token.last_used_at) : t('Not used yet')}</dd></div>{token.expires_at && <div><dt>{t('Expires')}</dt><dd>{dateLabel(token.expires_at)}</dd></div>}</dl>
            </>}
            <div className='api-token-actions'>
              {active ? <>
                <button className='button secondary' disabled={busy} onClick={() => confirm('rotate')}><RefreshCw aria-hidden='true' />{t('Regenerate')}</button>
                <button className='button secondary api-token-danger' disabled={busy} onClick={() => confirm('revoke')}><Trash2 aria-hidden='true' />{t('Revoke token')}</button>
              </> : <button className='button secondary' disabled={busy} onClick={() => void mutate('generate')}>{busy ? <RefreshCw className='spin' aria-hidden='true' /> : <KeyRound aria-hidden='true' />}{busy ? t('Generating…') : t('Generate token')}</button>}
            </div>
          </div>
        </> : null}
        {notice && <p className='api-token-notice' role='status'><Check aria-hidden='true' />{translateMessage(notice)}</p>}
        <span className='sr-only' role='status'>{copied ? t('Copied to clipboard.') : ''}</span>
        {copyError && <p className='account-help' role='status'>{translateMessage(copyError)}</p>}
        {!confirmation && <ErrorNotice>{error && !loading && !data
          ? <>{localizedError(error)} {t('Reload the token status before trying again.')}</>
          : error}</ErrorNotice>}
        {!loading && !data && <button className='button secondary' disabled={busy} onClick={() => setAttempt(value => value + 1)}><RefreshCw aria-hidden='true' />{t('Reload token status')}</button>}
      </div>
    </div>
    {confirmation && <ConfirmTokenAction action={confirmation} busy={busy} error={error} onClose={() => { if (!submitting.current) setConfirmation(null); }} onConfirm={() => void mutate(confirmation)} />}
  </section>;
}
