import { Check, Mail, UserRound, X } from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent, type MutableRefObject } from 'react';
import { APIError, errorMessage, request, type AuthStatus, type CodeDelivery } from '../api';
import { ErrorNotice, Loading, PasswordField } from '../ui';
import { passwordWithinLimit, validCode, validEmail } from '../validation';
import type { AdminUser } from './types';

type Field = 'email' | 'code' | 'password' | 'confirmation';

export default function CreateUserDialog({ deadlines, onClose, onSaved }: {
  deadlines: MutableRefObject<Record<string, number>>;
  onClose: () => void;
  onSaved: (user: AdminUser) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const form = useRef<HTMLFormElement>(null);
  const emailInput = useRef<HTMLInputElement>(null);
  const codeInput = useRef<HTMLInputElement>(null);
  const mounted = useRef(true);
  const inFlight = useRef(false);
  const [status, setStatus] = useState<AuthStatus | null>(null);
  const [statusLoading, setStatusLoading] = useState(true);
  const [statusError, setStatusError] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [email, setEmail] = useState('');
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const [confirmation, setConfirmation] = useState('');
  const [error, setError] = useState('');
  const [fieldError, setFieldError] = useState<Partial<Record<Field, string>>>({});
  const [busy, setBusy] = useState(false);
  const [sending, setSending] = useState(false);
  const [sent, setSent] = useState<{ email: string; expiresAt: number; expiresIn: number } | null>(null);
  const [now, setNow] = useState(Date.now());
  const normalizedEmail = email.trim().toLowerCase();
  const remaining = Math.max(0, Math.ceil(((deadlines.current[normalizedEmail] ?? 0) - now) / 1000));
  const registrationClosed = status?.registration_enabled === false;
  const disabled = busy || sending || statusLoading || !status || registrationClosed;

  useEffect(() => {
    mounted.current = true;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const overflow = document.body.style.overflow;
    const currentDialog = dialog.current;
    document.body.style.overflow = 'hidden';
    currentDialog?.showModal();
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => {
      mounted.current = false;
      window.clearInterval(timer);
      currentDialog?.close();
      document.body.style.overflow = overflow;
      previous?.focus();
    };
  }, []);

  useEffect(() => {
    const abort = new AbortController();
    setStatusLoading(true); setStatusError('');
    request<AuthStatus>('/auth/status', { signal: abort.signal })
      .then(result => { if (!abort.signal.aborted) setStatus(result); })
      .catch(cause => { if (!abort.signal.aborted) setStatusError(errorMessage(cause)); })
      .finally(() => { if (!abort.signal.aborted) setStatusLoading(false); });
    return () => abort.abort();
  }, [attempt]);

  useEffect(() => {
    if (!statusLoading && status?.registration_enabled) emailInput.current?.focus();
  }, [statusLoading, status?.registration_enabled]);

  useEffect(() => {
    if (busy || sending) return;
    const field = Object.keys(fieldError).find(key => fieldError[key as Field]);
    const input = field ? form.current?.elements.namedItem(field) : null;
    if (input instanceof HTMLInputElement) input.focus();
  }, [busy, sending, fieldError]);

  function close() { if (!inFlight.current) onClose(); }
  function cooldown(seconds: number, key: string) {
    deadlines.current[key] = Date.now() + Math.max(0, seconds) * 1000;
    setNow(Date.now());
  }
  function invalid(field: Field, message: string) {
    setFieldError({ [field]: message });
    const input = form.current?.elements.namedItem(field);
    if (input instanceof HTMLInputElement) input.focus();
  }
  function clearError(field: Field) {
    setFieldError(current => ({ ...current, [field]: undefined }));
    setError('');
  }
  function handleError(cause: unknown) {
    if (cause instanceof APIError) {
      if (cause.code === 'REGISTRATION_DISABLED') {
        setStatus(current => current && { ...current, registration_enabled: false });
        return;
      }
      if (cause.code === 'EMAIL_ALREADY_REGISTERED') {
        invalid('email', 'This email is already registered. Use Add admin to grant administrator access.');
        return;
      }
      if (cause.code === 'INVALID_CODE') {
        invalid('code', errorMessage(cause));
        return;
      }
    }
    setError(errorMessage(cause));
  }
  async function sendCode() {
    if (inFlight.current || disabled || remaining > 0) return;
    setError(''); setFieldError({});
    if (!validEmail(normalizedEmail)) {
      invalid('email', 'Enter a valid email address of 50 characters or fewer.'); return;
    }
    const deliveryEmail = normalizedEmail;
    setEmail(deliveryEmail);
    setSent(null);
    inFlight.current = true; setSending(true);
    try {
      const result = await request<CodeDelivery>('/admin/users/registration-code', { method: 'POST', body: { email: deliveryEmail } });
      if (!mounted.current) return;
      cooldown(result.retry_after || status!.email_send_cooldown_seconds, deliveryEmail);
      setSent({ email: deliveryEmail, expiresAt: Date.now() + result.expires_in * 1000, expiresIn: result.expires_in });
      setCode('');
    } catch (cause) {
      if (!mounted.current) return;
      handleError(cause);
      if (cause instanceof APIError && cause.retryAfter) cooldown(cause.retryAfter, deliveryEmail);
    } finally {
      inFlight.current = false;
      if (mounted.current) setSending(false);
    }
  }
  useEffect(() => {
    if (!sending && sent?.email === normalizedEmail) codeInput.current?.focus();
  }, [sending, sent]);

  async function create(event: FormEvent) {
    event.preventDefault();
    if (inFlight.current || disabled) return;
    setError(''); setFieldError({});
    if (!validEmail(normalizedEmail)) { invalid('email', 'Enter a valid email address of 50 characters or fewer.'); return; }
    if (!validCode(code.trim())) { invalid('code', 'Enter the 6-character code from the email. Letters and numbers are accepted.'); return; }
    if (Array.from(password).length < 8) { invalid('password', 'Use a password with at least 8 characters.'); return; }
    if (!passwordWithinLimit(password)) { invalid('password', 'Password must be at most 72 UTF-8 bytes. Use a shorter password.'); return; }
    if (password !== confirmation) { invalid('confirmation', 'The passwords do not match.'); return; }
    inFlight.current = true; setBusy(true); setEmail(normalizedEmail);
    try {
      const result = await request<{ user: AdminUser }>('/admin/users', {
        method: 'POST', body: { username: normalizedEmail, email: normalizedEmail, password, code: code.trim() },
      });
      if (mounted.current) onSaved(result.user);
    } catch (cause) {
      if (mounted.current) handleError(cause);
    } finally {
      inFlight.current = false;
      if (mounted.current) setBusy(false);
    }
  }
  const inputError = (field: Field) => fieldError[field] && <p className='field-error' id={`create-user-${field}-error`} role='alert'>{fieldError[field]}</p>;
  return <dialog ref={dialog} className='admin-create-dialog' aria-labelledby='create-user-title' aria-describedby='create-user-description' onCancel={event => { event.preventDefault(); close(); }}>
    <header className='admin-create-heading'>
      <div><h2 id='create-user-title'>Create account</h2><p id='create-user-description'>Register a new member with email verification.</p></div>
      <button className='admin-dialog-close' type='button' aria-label='Close create account' disabled={busy || sending} onClick={close}><X aria-hidden='true' /></button>
    </header>
    <form ref={form} onSubmit={create} noValidate aria-busy={busy || sending}>
      <div className='admin-create-fields'>
        {statusLoading && <Loading label='Loading registration settings…' />}
        {statusError && <div><ErrorNotice>{statusError}</ErrorNotice><button type='button' className='text-button' onClick={() => setAttempt(value => value + 1)}>Try again</button></div>}
        {registrationClosed && <div className='notice information' role='status'>Registration is currently disabled. Enable it in Config → Access to create an account.</div>}
        <div className='field'>
          <label htmlFor='create-user-email'>Email address</label>
          <input ref={emailInput} id='create-user-email' name='email' type='email' autoComplete='off' autoCapitalize='none' spellCheck={false} maxLength={50} required value={email} disabled={disabled} placeholder='name@example.com' aria-invalid={!!fieldError.email} aria-describedby={`create-user-email-help${fieldError.email ? ' create-user-email-error' : ''}`} onChange={event => { setEmail(event.target.value); setCode(''); setSent(null); clearError('email'); clearError('code'); }} />
          <p className='admin-create-help' id='create-user-email-help'>This email address will be the username.</p>
          {inputError('email')}
        </div>
        <div className='field'>
          <label htmlFor='create-user-code'>Verification code</label>
          <div className='code-row'>
            <input ref={codeInput} id='create-user-code' name='code' type='text' autoComplete='one-time-code' autoCapitalize='none' autoCorrect='off' spellCheck={false} maxLength={6} required value={code} disabled={disabled} placeholder='6-character code' aria-invalid={!!fieldError.code} aria-describedby={fieldError.code ? 'create-user-code-error' : undefined} onChange={event => { setCode(event.target.value.replace(/[^a-zA-Z0-9]/g, '')); clearError('code'); }} />
            <button className='button pale' type='button' disabled={disabled || remaining > 0 || !validEmail(normalizedEmail)} onClick={sendCode}><Mail aria-hidden='true' />{sending ? 'Sending…' : remaining ? `Resend in ${remaining}s` : sent?.email === normalizedEmail ? 'Resend code' : 'Send code'}</button>
          </div>
          {sent?.email === normalizedEmail && <p className='code-status' role='status'><Check aria-hidden='true' />{now >= sent.expiresAt ? 'The code has expired. Request a new code.' : `Code sent to this email. Valid for ${Math.ceil(sent.expiresIn / 60)} minutes.`}</p>}
          {inputError('code')}
        </div>
        <div>
          <PasswordField label='Password' name='password' required autoComplete='new-password' value={password} disabled={disabled} placeholder='At least 8 characters' aria-invalid={!!fieldError.password} aria-describedby={fieldError.password ? 'create-user-password-error' : undefined} onChange={event => { setPassword(event.target.value); clearError('password'); }} />
          {inputError('password')}
        </div>
        <div>
          <PasswordField label='Confirm password' name='confirmation' required autoComplete='new-password' value={confirmation} disabled={disabled} placeholder='Re-enter password' aria-invalid={!!fieldError.confirmation} aria-describedby={fieldError.confirmation ? 'create-user-confirmation-error' : undefined} onChange={event => { setConfirmation(event.target.value); clearError('confirmation'); }} />
          {inputError('confirmation')}
        </div>
        <ErrorNotice>{error}</ErrorNotice>
        <div className='admin-create-note'><UserRound aria-hidden='true' /><p>New accounts are created as members.<br />Your administrator session stays signed in.</p></div>
      </div>
      <footer className='admin-create-actions'><button type='button' className='button secondary' disabled={busy || sending} onClick={close}>Cancel</button><button type='submit' className='button primary' disabled={disabled}>{busy ? 'Creating…' : 'Create account'}</button></footer>
    </form>
  </dialog>;
}
