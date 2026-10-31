import { Check, Copy, KeyRound, RefreshCw, ShieldCheck, Trash2, X } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { errorMessage, request } from '../api';
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
};
type TokenResponse = { token: Token | null; plaintext_token?: string };
type Action = 'generate' | 'rotate' | 'revoke';
const tokenPath = '/account/api-token';
const endpointPath = '/tk-register/api/v1/vendors/tk-register-0/claude/accounts';

function dateLabel(value: string) {
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value));
}

function ConfirmTokenAction({ action, busy, error, onClose, onConfirm }: {
  action: 'rotate' | 'revoke'; busy: boolean; error: string;
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
      <h2 id='token-dialog-title'>{revoking ? 'Revoke API token?' : 'Regenerate API token?'}</h2>
      <button type='button' className='api-token-icon-button' aria-label='Close dialog' disabled={busy} onClick={onClose}><X aria-hidden='true' /></button>
    </div>
    <p id='token-dialog-description'>{revoking
      ? 'This token will stop working immediately. You can generate a new token when you need API access again.'
      : 'Your current token will stop working immediately. Copy the new token and update any scripts that use it.'}</p>
    <ErrorNotice>{error}</ErrorNotice>
    <div className='api-token-dialog-actions'>
      <button ref={cancel} type='button' className='button secondary' disabled={busy} onClick={onClose}>Cancel</button>
      <button type='button' className={`button secondary ${revoking ? 'api-token-danger' : ''}`} disabled={busy} onClick={onConfirm}>
        {busy ? <RefreshCw className='spin' aria-hidden='true' /> : revoking ? <Trash2 aria-hidden='true' /> : <RefreshCw aria-hidden='true' />}
        {busy ? 'Saving…' : revoking ? 'Revoke token' : 'Regenerate'}
      </button>
    </div>
  </dialog>;
}

export default function APIToken() {
  const [data, setData] = useState<TokenResponse | null>(null);
  const [plaintext, setPlaintext] = useState('');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [copied, setCopied] = useState<'token' | 'endpoint' | null>(null);
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
      .catch(cause => { if (!controller.signal.aborted) setError(errorMessage(cause)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [attempt]);

  const token = data?.token;
  const expired = Boolean(token?.expires_at && new Date(token.expires_at).getTime() <= Date.now());
  const active = Boolean(token && !token.revoked_at && !expired);
  const endpoint = `${window.location.origin}${endpointPath}`;

  async function copy(value: string, target: 'token' | 'endpoint') {
    setCopyError('');
    try {
      await navigator.clipboard.writeText(value);
      if (!mounted.current) return;
      setCopied(target);
      clearTimeout(copyTimer.current);
      copyTimer.current = setTimeout(() => setCopied(null), 2500);
    } catch {
      if (!mounted.current) return;
      if (target === 'token') { tokenInput.current?.focus(); tokenInput.current?.select(); }
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
    setCopied(null);
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
      setError(errorMessage(cause));
      setConfirmation(null);
      try {
        const current = await request<TokenResponse>(tokenPath);
        if (mounted.current) setData({ token: current.token });
      } catch {
        if (mounted.current) {
          setData(null);
          setError(`${errorMessage(cause)} Reload the token status before trying again.`);
        }
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
    <h2 id='api-token-title'>API token</h2>
    <div className='account-row'>
      <div className='account-label'>Personal access</div>
      <div className='account-value api-token-content' aria-busy={busy || loading}>
        <p className='api-token-description'>Submit accounts and read saved status and usage for your own accounts. API submissions appear in your submission history.</p>
        {loading ? <Loading label='Loading token…' /> : data ? <>
          <div className={`api-token-card ${plaintext && active ? 'has-plaintext' : ''}`}>
            <div className='api-token-card-heading'>
              <span className='api-token-key'><KeyRound aria-hidden='true' /></span>
              <div><h3>{active ? 'Your API token' : token?.revoked_at ? 'Token revoked' : expired ? 'Token expired' : 'No API token yet'}</h3><p>{active ? 'Submission and account lookup access' : 'Generate a token to submit accounts and read your saved account data.'}</p></div>
              {active && <span className='verified'><ShieldCheck aria-hidden='true' />Active</span>}
            </div>
            {active && token && <>
              <div className='api-token-value-row'>
                <input ref={tokenInput} className='api-token-value' aria-label={plaintext ? 'New API token' : 'Masked API token'} value={plaintext || `${token.prefix}••••••••••••${token.last_four}`} readOnly autoComplete='off' spellCheck={false} />
                {plaintext && <button type='button' className='api-token-icon-button' onClick={() => void copy(plaintext, 'token')} aria-label={copied === 'token' ? 'Token copied' : 'Copy API token'} title={copied === 'token' ? 'Copied' : 'Copy token'}>{copied === 'token' ? <Check aria-hidden='true' /> : <Copy aria-hidden='true' />}</button>}
              </div>
              <p className='account-help api-token-secret-help'>{plaintext ? 'Copy and save this token securely. Leaving this page hides it.' : 'For security, only the beginning and end are shown. Regenerate if you need a new token.'}</p>
              <dl className='api-token-metadata'><div><dt>Created</dt><dd>{dateLabel(token.created_at)}</dd></div><div><dt>Last used</dt><dd>{token.last_used_at ? dateLabel(token.last_used_at) : 'Not used yet'}</dd></div>{token.expires_at && <div><dt>Expires</dt><dd>{dateLabel(token.expires_at)}</dd></div>}</dl>
            </>}
            <div className='api-token-actions'>
              {active ? <>
                <button className='button secondary' disabled={busy} onClick={() => confirm('rotate')}><RefreshCw aria-hidden='true' />Regenerate</button>
                <button className='button secondary api-token-danger' disabled={busy} onClick={() => confirm('revoke')}><Trash2 aria-hidden='true' />Revoke token</button>
              </> : <button className='button secondary' disabled={busy} onClick={() => void mutate('generate')}>{busy ? <RefreshCw className='spin' aria-hidden='true' /> : <KeyRound aria-hidden='true' />}{busy ? 'Generating…' : 'Generate token'}</button>}
            </div>
          </div>
          <div className='api-token-endpoint'>
            <span className='api-token-endpoint-label'>Submission endpoint</span>
            <div className='api-token-endpoint-row'><span className='api-token-method'>POST</span><code>{endpoint}</code><button className='api-token-icon-button' type='button' aria-label={copied === 'endpoint' ? 'Endpoint copied' : 'Copy endpoint'} onClick={() => void copy(endpoint, 'endpoint')} title={copied === 'endpoint' ? 'Copied' : 'Copy endpoint'}>{copied === 'endpoint' ? <Check aria-hidden='true' /> : <Copy aria-hidden='true' />}</button></div>
            <p className='account-help'>Send JSON with <code>Authorization: Bearer &lt;your token&gt;</code> and <code>Content-Type: application/json</code>.</p>
          </div>
        </> : null}
        {notice && <p className='api-token-notice' role='status'><Check aria-hidden='true' />{notice}</p>}
        <span className='sr-only' role='status'>{copied ? 'Copied to clipboard.' : ''}</span>
        {copyError && <p className='account-help' role='status'>{copyError}</p>}
        {!confirmation && <ErrorNotice>{error}</ErrorNotice>}
        {!loading && !data && <button className='button secondary' disabled={busy} onClick={() => setAttempt(value => value + 1)}><RefreshCw aria-hidden='true' />Reload token status</button>}
      </div>
    </div>
    {confirmation && <ConfirmTokenAction action={confirmation} busy={busy} error={error} onClose={() => { if (!submitting.current) setConfirmation(null); }} onConfirm={() => void mutate(confirmation)} />}
  </section>;
}
