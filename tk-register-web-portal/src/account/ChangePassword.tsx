import { Check, LockKeyhole, LogOut, Mail, X } from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent, type MutableRefObject } from 'react';
import { APIError, errorMessage, request, retryAfterSeconds } from '../api';
import { ErrorNotice, PasswordField } from '../ui';
import { passwordWithinLimit } from '../validation';
import './change-password.css';

type Field = 'code' | 'password' | 'confirmation';
type Delivery = { challenge_id: string; retry_after: number; expires_in: number; code_length: number };
type Challenge = Delivery & { expiresAt: number };
type Operation = { challenge_id: string; code: string; password: string; confirmation: string; request_id: string };

function requestID() {
  // getRandomValues also works on the existing HTTP installation.
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 15) | 64;
  bytes[8] = (bytes[8] & 63) | 128;
  const value = Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('');
  return `${value.slice(0, 8)}-${value.slice(8, 12)}-${value.slice(12, 16)}-${value.slice(16, 20)}-${value.slice(20)}`;
}

function ChangePasswordDialog({ email, deadline, onClose, onChanged }: {
  email: string; deadline: MutableRefObject<number>; onClose: () => void; onChanged: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const form = useRef<HTMLFormElement>(null);
  const codeInput = useRef<HTMLInputElement>(null);
  const mounted = useRef(false);
  const inFlight = useRef(false);
  const activeRequest = useRef<AbortController | null>(null);
  const operation = useRef<Operation | null>(null);
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const [confirmation, setConfirmation] = useState('');
  const [challenge, setChallenge] = useState<Challenge | null>(null);
  const [error, setError] = useState('');
  const [errors, setErrors] = useState<Partial<Record<Field, string>>>({});
  const [busy, setBusy] = useState(false);
  const [sending, setSending] = useState(false);
  const [retryAt, setRetryAt] = useState(0);
  const [now, setNow] = useState(Date.now());
  const remaining = Math.max(0, Math.ceil((deadline.current - now) / 1000));
  const retryRemaining = Math.max(0, Math.ceil((retryAt - now) / 1000));
  const codeLength = challenge?.code_length || 6;
  const expired = !!challenge && now >= challenge.expiresAt;
  const disabled = busy || sending;

  useEffect(() => {
    mounted.current = true;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const overflow = document.body.style.overflow;
    const element = dialog.current;
    document.body.style.overflow = 'hidden';
    element?.showModal();
    codeInput.current?.focus();
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => {
      mounted.current = false;
      activeRequest.current?.abort();
      operation.current = null;
      window.clearInterval(timer);
      element?.close();
      document.body.style.overflow = overflow;
      if (previous?.isConnected) previous.focus({ preventScroll: true });
    };
  }, []);

  useEffect(() => {
    if (disabled) return;
    const name = (Object.keys(errors) as Field[]).find(field => errors[field]);
    const input = name && form.current?.elements.namedItem(name);
    if (input instanceof HTMLInputElement) input.focus();
  }, [disabled, errors]);

  function clearSecrets() {
    operation.current = null;
    setCode(''); setPassword(''); setConfirmation(''); setChallenge(null);
  }
  function close() {
    if (inFlight.current) return;
    activeRequest.current?.abort();
    clearSecrets();
    onClose();
  }
  function keepFocus(event: KeyboardEvent<HTMLDialogElement>) {
    if (event.key !== 'Tab') return;
    const controls = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), [tabindex="0"]')).filter(element => element.getClientRects().length > 0);
    const first = controls[0]; const last = controls.at(-1);
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
  }
  function edited(field: Field) {
    operation.current = null;
    setErrors(value => ({ ...value, [field]: undefined }));
    setError('');
  }
  function invalid(field: Field, message: string) { setErrors({ [field]: message }); }
  function cooldown(seconds: number) {
    deadline.current = Date.now() + retryAfterSeconds(seconds) * 1000;
    setNow(Date.now());
  }
  function handleError(cause: unknown, submitting: boolean) {
    if (cause instanceof APIError) {
      if (cause.retryAfter) {
        if (submitting) setRetryAt(Date.now() + cause.retryAfter * 1000);
        else cooldown(cause.retryAfter);
      }
      if (submitting && cause.status > 0 && cause.status < 500 && cause.status !== 429) operation.current = null;
      if (cause.code === 'INVALID_CODE') { invalid('code', errorMessage(cause)); return; }
      if (cause.code === 'INVALID_PASSWORD' || cause.code === 'PASSWORD_UNCHANGED') { invalid('password', errorMessage(cause)); return; }
      if (cause.code === 'PASSWORD_MISMATCH') { invalid('confirmation', errorMessage(cause)); return; }
      if (cause.code === 'PASSWORD_CHANGE_CONFLICT') {
        operation.current = null;
        setCode(''); setChallenge(null);
        invalid('code', 'This password change is no longer available. Send a new verification code.');
        return;
      }
    }
    setError(errorMessage(cause));
  }
  async function sendCode() {
    if (inFlight.current || remaining > 0) return;
    inFlight.current = true; setSending(true); setError(''); setErrors({});
    const controller = new AbortController(); activeRequest.current = controller;
    try {
      const result = await request<Delivery>('/account/password/code', { method: 'POST', body: {}, signal: controller.signal });
      if (!mounted.current || controller.signal.aborted) return;
      operation.current = null;
      setCode('');
      setChallenge({ ...result, expiresAt: Date.now() + result.expires_in * 1000 });
      cooldown(result.retry_after);
      requestAnimationFrame(() => { if (mounted.current && !controller.signal.aborted) codeInput.current?.focus(); });
    } catch (cause) {
      if (mounted.current && !controller.signal.aborted) handleError(cause, false);
    } finally {
      inFlight.current = false;
      if (mounted.current && !controller.signal.aborted) setSending(false);
    }
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (inFlight.current || retryRemaining > 0) return;
    setError(''); setErrors({});
    if (!challenge) { invalid('code', 'Send a verification code to your email first.'); return; }
    if (expired && !operation.current) { invalid('code', 'The code has expired. Send a new verification code.'); return; }
    if (!new RegExp(`^[a-zA-Z0-9]{${codeLength}}$`).test(code.trim())) { invalid('code', `Enter the ${codeLength}-character verification code from your email.`); return; }
    if (Array.from(password).length < 8) { invalid('password', 'Use a password with at least 8 characters.'); return; }
    if (!passwordWithinLimit(password)) { invalid('password', 'Password must be at most 72 UTF-8 bytes. Use a shorter password.'); return; }
    if (password !== confirmation) { invalid('confirmation', 'The passwords do not match.'); return; }
    const body = operation.current || { challenge_id: challenge.challenge_id, code: code.trim(), password, confirmation, request_id: requestID() };
    operation.current = body;
    const controller = new AbortController(); activeRequest.current = controller;
    inFlight.current = true; setBusy(true);
    try {
      const result = await request<{ password_changed: boolean; sign_in_required: boolean }>('/account/password', { method: 'POST', body, signal: controller.signal });
      if (!mounted.current || controller.signal.aborted) return;
      if (!result.password_changed || !result.sign_in_required) throw new Error('The password change could not be confirmed. Please try again.');
      clearSecrets();
      onChanged();
    } catch (cause) {
      if (mounted.current && !controller.signal.aborted) handleError(cause, true);
    } finally {
      inFlight.current = false;
      if (mounted.current && !controller.signal.aborted) setBusy(false);
    }
  }
  const fieldError = (field: Field) => errors[field] && <p className='field-error' id={`change-password-${field}-error`} role='alert'>{errors[field]}</p>;
  return <dialog ref={dialog} className='admin-create-dialog password-change-dialog' aria-labelledby='change-password-title' aria-describedby='change-password-description' onKeyDown={keepFocus} onCancel={event => { event.preventDefault(); close(); }}>
    <header className='admin-create-heading'>
      <div><h2 id='change-password-title'>Change password</h2><p id='change-password-description'>Verify your email address to change your TK Register password.</p></div>
      <button className='admin-dialog-close' type='button' aria-label='Close change password' disabled={disabled} onClick={close}><X aria-hidden='true' /></button>
    </header>
    <form ref={form} onSubmit={submit} noValidate aria-busy={disabled}>
      <div className='admin-create-fields'>
        <div className='field'><label htmlFor='change-password-email'>Email address</label><div className='change-password-email'><input id='change-password-email' type='email' value={email} readOnly autoComplete='username' /><LockKeyhole aria-hidden='true' /></div></div>
        <div className='field'>
          <label htmlFor='change-password-code'>Verification code</label>
          <div className='code-row'>
            <input ref={codeInput} id='change-password-code' name='code' autoComplete='one-time-code' autoCapitalize='none' autoCorrect='off' spellCheck={false} maxLength={codeLength} placeholder={`${codeLength} characters`} value={code} disabled={disabled} aria-invalid={!!errors.code} aria-describedby={`change-password-code-status${errors.code ? ' change-password-code-error' : ''}`} onChange={event => { setCode(event.target.value); edited('code'); }} />
            <button className='button pale' type='button' disabled={disabled || remaining > 0} onClick={sendCode}><Mail aria-hidden='true' />{sending ? 'Sending…' : remaining ? `Resend in ${remaining}s` : challenge ? 'Resend code' : 'Send code'}</button>
          </div>
          <p className='code-status' id='change-password-code-status' role='status'>{challenge && <><Check aria-hidden='true' />{expired ? 'The code has expired. Send a new verification code.' : `Code sent to your email. Valid for ${Math.ceil(challenge.expires_in / 60)} minutes.`}</>}</p>
          {fieldError('code')}
        </div>
        <div><PasswordField label='New password' name='password' autoComplete='new-password' placeholder='At least 8 characters' value={password} disabled={disabled} aria-invalid={!!errors.password} aria-describedby={`change-password-help${errors.password ? ' change-password-password-error' : ''}`} onChange={event => { setPassword(event.target.value); edited('password'); }} /><p className='admin-create-help' id='change-password-help'>Use at least 8 characters, up to 72 UTF-8 bytes.</p>{fieldError('password')}</div>
        <div><PasswordField label='Confirm new password' name='confirmation' autoComplete='new-password' placeholder='Re-enter your new password' value={confirmation} disabled={disabled} aria-invalid={!!errors.confirmation} aria-describedby={errors.confirmation ? 'change-password-confirmation-error' : undefined} onChange={event => { setConfirmation(event.target.value); edited('confirmation'); }} />{fieldError('confirmation')}</div>
        <ErrorNotice>{error}</ErrorNotice>
        <div className='admin-create-note'><LogOut aria-hidden='true' /><p>You’ll be signed out of all browser sessions after changing your password.</p></div>
      </div>
      <footer className='admin-create-actions'><button type='button' className='button secondary' disabled={disabled} onClick={close}>Cancel</button><button type='submit' className='button primary' disabled={disabled || retryRemaining > 0 || (remaining > 0 && !challenge)}>{busy ? 'Changing password…' : retryRemaining ? `Try again in ${retryRemaining}s` : 'Change password'}</button></footer>
    </form>
  </dialog>;
}

export default function ChangePassword({ email, onChanged }: { email: string; onChanged: () => void }) {
  const [open, setOpen] = useState(false);
  const deadline = useRef(0);
  return <>
    <div className='account-row'>
      <div className='account-label'>Password</div>
      <div className='account-value change-password-row'><div><span aria-label='Password is set'>••••••••••</span><p className='account-help'>Verify your email to change your password.</p></div><button type='button' className='button secondary' onClick={() => setOpen(true)}>Change password</button></div>
    </div>
    {open && <ChangePasswordDialog email={email} deadline={deadline} onClose={() => setOpen(false)} onChanged={onChanged} />}
  </>;
}
