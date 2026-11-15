import { Check, Link2, Mail, ShieldCheck, UsersRound, X } from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent, type MutableRefObject } from 'react';
import { APIError, errorMessage, request, type CodeDelivery } from '../api';
import { ErrorNotice } from '../ui';
import { validEmail } from '../validation';

export default function AddSubAccountsDialog({ currentEmail, directEmails, deadlines, onClose, onSaved }: {
  currentEmail: string;
  directEmails: string[];
  deadlines: MutableRefObject<Record<string, number>>;
  onClose: () => void;
  onSaved: (user: { id: string; email: string }) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const emailInput = useRef<HTMLInputElement>(null);
  const codeInput = useRef<HTMLInputElement>(null);
  const [email, setEmail] = useState('');
  const [code, setCode] = useState('');
  const [emailError, setEmailError] = useState('');
  const [codeError, setCodeError] = useState('');
  const mounted = useRef(true);
  const inFlight = useRef(false);
  const [error, setError] = useState('');
  const [sending, setSending] = useState(false);
  const [saving, setSaving] = useState(false);
  const [sent, setSent] = useState<{ email: string; expiresAt: number } | null>(null);
  const [now, setNow] = useState(Date.now());
  const busy = sending || saving;
  const normalizedEmail = email.trim().toLowerCase();

  const remaining = Math.max(0, Math.ceil(((deadlines.current[normalizedEmail] ?? 0) - now) / 1000));

  useEffect(() => {
    mounted.current = true;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const overflow = document.body.style.overflow;
    const element = dialog.current;
    document.body.style.overflow = 'hidden';
    element?.showModal();
    emailInput.current?.focus();
    return () => { mounted.current = false; window.clearInterval(timer); element?.close(); document.body.style.overflow = overflow; previous?.focus(); };
  }, []);

  function checkEmail() {
    let error = '';
    if (!validEmail(normalizedEmail)) error = 'Enter a valid email address of 50 characters or fewer.';
    else if (normalizedEmail === currentEmail.trim().toLowerCase()) error = 'You cannot add yourself as a sub account.';
    else if (directEmails.some(value => value.toLowerCase() === normalizedEmail)) error = 'This user is already your direct member.';
    setEmailError(error);
    if (error) emailInput.current?.focus();
    return !error;
  }
  function close() { if (!inFlight.current) onClose(); }
  function cooldown(seconds: number, target: string) {
    deadlines.current[target] = Date.now() + seconds * 1000;
    setNow(Date.now());
  }
  function showError(cause: unknown) {
    if (cause instanceof APIError && cause.code === 'INVALID_CODE') {
      setCodeError(cause.message); requestAnimationFrame(() => codeInput.current?.focus());
    } else if (cause instanceof APIError && cause.code.startsWith('MEMBER_')) {
      setEmailError(cause.message);
    } else { setError(errorMessage(cause)); }
  }
  async function sendCode() {
    if (inFlight.current || remaining > 0 || !checkEmail()) return;
    const target = normalizedEmail;
    inFlight.current = true; setSending(true); setEmail(target); setError(''); setCodeError(''); setSent(null); setCode('');
    try {
      const result = await request<CodeDelivery>('/members/link-code', { method: 'POST', body: { email: target } });
      if (!mounted.current) return;
      cooldown(result.retry_after, target);
      setSent({ email: target, expiresAt: Date.now() + result.expires_in * 1000 });
    } catch (cause) {
      if (mounted.current) {
        showError(cause);
        if (cause instanceof APIError && cause.retryAfter) cooldown(cause.retryAfter, target);
      }
    } finally { inFlight.current = false; if (mounted.current) setSending(false); }
  }
  useEffect(() => {
    if (!sending && sent?.email === normalizedEmail) codeInput.current?.focus();
  }, [sending, sent, normalizedEmail]);
  async function link(event: FormEvent) {
    event.preventDefault();
    if (inFlight.current || !checkEmail()) return;
    if (!code.trim()) { setCodeError('Enter the verification code from the email.'); codeInput.current?.focus(); return; }
    inFlight.current = true; setSaving(true); setError(''); setCodeError('');
    try {
      const result = await request<{ user: { id: string; email: string } }>('/members/link', { method: 'POST', body: { email: normalizedEmail, code: code.trim() } });
      if (mounted.current) onSaved(result.user);
    } catch (cause) { if (mounted.current) showError(cause); }
    finally { inFlight.current = false; if (mounted.current) setSaving(false); }
  }

  return <dialog ref={dialog} className='admin-create-dialog member-link-dialog' aria-labelledby='member-link-title' aria-describedby='member-link-description' onCancel={event => { event.preventDefault(); close(); }}>
    <header className='admin-create-heading'>
      <div><h2 id='member-link-title'>Add sub accounts</h2><p id='member-link-description'>Connect an existing TK Register user as your direct member.</p></div>
      <button type='button' className='admin-dialog-close' aria-label='Close add sub accounts' disabled={busy} onClick={close}><X aria-hidden='true' /></button>
    </header>
    <form onSubmit={link} noValidate aria-busy={busy}>
      <div className='admin-create-fields member-link-fields'>
          <div className='field'>
            <label htmlFor='member-link-email'>User email</label>
            <input ref={emailInput} id='member-link-email' type='email' autoComplete='off' autoCapitalize='none' spellCheck={false} maxLength={50} required disabled={busy} value={email} placeholder='Existing user’s email address' aria-invalid={!!emailError} aria-describedby={`member-link-email-help${emailError ? ' member-link-email-error' : ''}`} onChange={event => { setEmail(event.target.value); setEmailError(''); setCodeError(''); setCode(''); setSent(null); setError(''); }} />
            <p className='admin-create-help' id='member-link-email-help'>Use the email address they already use to sign in.</p>
            {emailError && <p className='field-error' id='member-link-email-error' role='alert'>{emailError}</p>}
          </div>
          <div className='field'>
            <label htmlFor='member-link-code'>Verification code</label>
            <div className='code-row'>
              <input ref={codeInput} id='member-link-code' type='text' autoComplete='off' autoCapitalize='none' autoCorrect='off' spellCheck={false} disabled={busy} maxLength={32} value={code} placeholder='Enter code' aria-invalid={!!codeError} aria-describedby={`member-link-code-help${codeError ? ' member-link-code-error' : ''}`} onChange={event => { setCode(event.target.value); setCodeError(''); }} />
              <button type='button' className='button pale' disabled={busy || !normalizedEmail || remaining > 0} onClick={sendCode}><Mail aria-hidden='true' />{sending ? 'Sending…' : remaining > 0 ? `Resend in ${remaining}s` : 'Send code'}</button>
            </div>
            <p className='admin-create-help' id='member-link-code-help'>The verification code goes to this user’s email address.</p>
            {sent?.email === normalizedEmail && <p className='admin-create-help' role='status'>{now < sent.expiresAt ? <><Check aria-hidden='true' size={14} /> Code sent. Ask the user to confirm the connection and share the code.</> : 'This code has expired. Request a new code.'}</p>}
            {codeError && <p className='field-error' id='member-link-code-error' role='alert'>{codeError}</p>}
          </div>
          <div className='member-link-details'>
            <div><UsersRound aria-hidden='true' /><p>You’ll be able to view accounts available through this user, including their members’ accounts.</p></div>
            <div><ShieldCheck aria-hidden='true' /><p>Users with an existing parent cannot be added here. Their sign-in details and account ownership stay unchanged.</p></div>
          </div>
        <ErrorNotice>{error}</ErrorNotice>
      </div>
      <footer className='admin-create-actions'>
        <button type='button' className='button secondary' disabled={busy} onClick={close}>Cancel</button>
        <button type='submit' className='button primary' disabled={busy || !normalizedEmail || !code.trim()}><Link2 aria-hidden='true' />{saving ? 'Adding…' : 'Add sub account'}</button>
      </footer>
    </form>
  </dialog>;
}
