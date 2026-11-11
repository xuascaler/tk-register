import { Check, Mail } from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import {
  APIError,
  errorMessage,
  request,
  type AuthStatus,
  type Challenge,
  type CodeDelivery,
  type Session,
} from './api';
import { ErrorNotice, PasswordField } from './ui';
import { passwordWithinLimit, validCode, validEmail } from './validation';

export default function Auth({
  status,
  onSession,
  message = '',
}: {
  status: AuthStatus;
  onSession: (session: Session) => void;
  message?: string;
}) {
  const [page, setPage] = useState<'login' | 'register'>('login');
  const [method, setMethod] = useState<'password' | 'code'>(
    status.password_login_enabled || !status.email_code_login_enabled
      ? 'password'
      : 'code',
  );
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [confirmation, setConfirmation] = useState('');
  const [code, setCode] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [sending, setSending] = useState(false);
  const [challenge, setChallenge] = useState<Challenge | null>(null);
  const [sent, setSent] = useState<{ key: string; expires: number } | null>(
    null,
  );
  const [deadlines, setDeadlines] = useState<Record<string, number>>({});
  const [now, setNow] = useState(Date.now());
  const inFlight = useRef(false);
  const codeInput = useRef<HTMLInputElement>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  const normalizedEmail = email.trim();
  const deliveryKey = challenge
    ? `challenge:${challenge.challenge_id}`
    : normalizedEmail.toLowerCase();
  const remaining = Math.max(
    0,
    Math.ceil(((deadlines[deliveryKey] ?? 0) - now) / 1000),
  );
  const isCode = page === 'register' || method === 'code' || !!challenge;
  const challengeExpired = !!challenge && now >= challenge.expires_in;

  function reset(nextPage: typeof page) {
    setPage(nextPage);
    setPassword('');
    setConfirmation('');
    setCode('');
    setChallenge(null);
    setError('');
  }
  function cooldown(seconds: number, key = deliveryKey) {
    setDeadlines((values) => ({
      ...values,
      [key]: Date.now() + Math.max(0, seconds) * 1000,
    }));
    setNow(Date.now());
  }
  async function sendCode() {
    if (sending || inFlight.current || remaining > 0) return;
    if (!challenge && !validEmail(normalizedEmail)) {
      setError('Enter a valid email address of 50 characters or fewer.');
      return;
    }
    setSending(true);
    setError('');
    inFlight.current = true;
    try {
      const result = await request<CodeDelivery>(
        challenge ? '/auth/admin/resend' : '/auth/code',
        {
          method: 'POST',
          body: challenge
            ? { challenge_id: challenge.challenge_id }
            : {
                email: normalizedEmail,
                purpose: page === 'register' ? 'register' : 'login',
              },
        },
      );
      if (!mounted.current) return;
      cooldown(result.retry_after || status.email_send_cooldown_seconds);
      setSent({ key: deliveryKey, expires: result.expires_in });
      setCode('');
      codeInput.current?.focus();
    } catch (cause) {
      if (!mounted.current) return;
      setError(errorMessage(cause));
      if (cause instanceof APIError && cause.retryAfter)
        cooldown(cause.retryAfter);
    } finally {
      inFlight.current = false;
      if (mounted.current) setSending(false);
    }
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (inFlight.current) return;
    setError('');
    if (!validEmail(normalizedEmail)) {
      setError('Enter a valid email address of 50 characters or fewer.');
      return;
    }
    if (isCode && !validCode(code.trim())) {
      setError(
        'Enter the 6-character code from your email. Letters and numbers are accepted.',
      );
      codeInput.current?.focus();
      return;
    }
    if (page === 'register' && password !== confirmation) {
      setError('The passwords do not match.');
      return;
    }
    if (page === 'register' && password.length < 8) {
      setError('Use a password with at least 8 characters.');
      return;
    }
    if (!challenge && !passwordWithinLimit(password)) {
      setError(
        'Password must be at most 72 UTF-8 bytes. Use a shorter password.',
      );
      return;
    }
    if (challengeExpired) {
      setError(
        'This sign-in attempt has expired. Sign in with your password again.',
      );
      return;
    }
    inFlight.current = true;
    setBusy(true);
    try {
      let result: Session | Challenge;
      if (challenge)
        result = await request<Session>('/auth/admin/verify', {
          method: 'POST',
          body: { challenge_id: challenge.challenge_id, code: code.trim() },
        });
      else if (page === 'register')
        result = await request<Session>('/auth/register', {
          method: 'POST',
          body: {
            username: normalizedEmail,
            email: normalizedEmail,
            password,
            code: code.trim(),
          },
        });
      else
        result = await request<Session | Challenge>('/auth/login', {
          method: 'POST',
          body: {
            email: normalizedEmail,
            method,
            ...(method === 'password' ? { password } : { code: code.trim() }),
          },
        });
      if (!mounted.current) return;
      setPassword('');
      setConfirmation('');
      setCode('');
      if ('challenge_required' in result && result.challenge_required) {
        setChallenge({
          ...result,
          expires_in: Date.now() + result.expires_in * 1000,
        });
        cooldown(
          result.retry_after || status.email_send_cooldown_seconds,
          `challenge:${result.challenge_id}`,
        );
        setSent({
          key: `challenge:${result.challenge_id}`,
          expires: result.code_expires_in,
        });
        setTimeout(() => codeInput.current?.focus(), 0);
      } else onSession(result as Session);
    } catch (cause) {
      if (mounted.current) setError(errorMessage(cause));
    } finally {
      inFlight.current = false;
      if (mounted.current) setBusy(false);
    }
  }
  const duration = sent?.expires ? Math.ceil(sent.expires / 60) : 10;
  return (
    <main className='auth-stage'>
      <section className='auth-panel' aria-labelledby='auth-title'>
        {challenge && (
          <p className='step-label'>
            <strong>Password verified</strong> · Step 2 of 2
          </p>
        )}
        <h1 id='auth-title'>
          {challenge
            ? 'Check your email'
            : page === 'register'
              ? 'Create your account'
              : 'Sign in'}
        </h1>
        {message && !challenge && (
          <div className='notice information' role='status'>
            {message}
          </div>
        )}
        {challenge ? (
          <p className='auth-intro'>
            Enter the verification code sent to{' '}
            <strong>{normalizedEmail}</strong>
          </p>
        ) : (
          page === 'login' &&
          status.email_code_login_enabled && (
            <div className='segment' aria-label='Sign-in method'>
              <button
                type='button'
                aria-pressed={method === 'password'}
                disabled={busy || sending}
                onClick={() => {
                  setMethod('password');
                  setError('');
                  setCode('');
                }}
              >
                {status.password_login_enabled ? 'Password' : 'Admin password'}
              </button>
              <button
                type='button'
                aria-pressed={method === 'code'}
                disabled={busy || sending}
                onClick={() => {
                  setMethod('code');
                  setPassword('');
                  setError('');
                }}
              >
                Email code
              </button>
            </div>
          )
        )}
        {!challenge &&
          page === 'login' &&
          method === 'password' &&
          !status.password_login_enabled && (
            <p className='admin-only-help'>
              Password sign-in is available to administrators. Members can use
              an email code.
            </p>
          )}
        <form className='auth-form' onSubmit={submit}>
          {!challenge && (
            <div className='field'>
              <label htmlFor='email'>Email address</label>
              <input
                id='email'
                name='email'
                type='email'
                autoComplete='username'
                maxLength={50}
                required
                value={email}
                disabled={busy || sending}
                onChange={(event) => {
                  setEmail(event.target.value);
                  setCode('');
                  setError('');
                }}
              />
            </div>
          )}
          {isCode && (
            <div className='field'>
              <label htmlFor='verification-code'>Verification code</label>
              <div className='code-row'>
                <input
                  ref={codeInput}
                  id='verification-code'
                  name='code'
                  type='text'
                  inputMode='text'
                  autoCapitalize='off'
                  autoCorrect='off'
                  spellCheck={false}
                  autoComplete='one-time-code'
                  maxLength={6}
                  required
                  value={code}
                  disabled={busy || challengeExpired}
                  onChange={(event) =>
                    setCode(event.target.value.replace(/[^a-zA-Z0-9]/g, ''))
                  }
                />
                <button
                  className='button pale'
                  type='button'
                  disabled={
                    busy ||
                    sending ||
                    remaining > 0 ||
                    challengeExpired ||
                    (!challenge && !validEmail(normalizedEmail))
                  }
                  onClick={sendCode}
                >
                  <Mail aria-hidden='true' />
                  {sending
                    ? 'Sending…'
                    : remaining
                      ? `Resend in ${remaining}s`
                      : sent?.key === deliveryKey
                        ? 'Resend code'
                        : 'Send code'}
                </button>
              </div>
              {sent?.key === deliveryKey && (
                <p className='code-status' role='status'>
                  <Check aria-hidden='true' />
                  {challengeExpired
                    ? 'The sign-in attempt has expired.'
                    : `Code requested. Check your inbox. Valid for ${duration} minutes.`}
                </p>
              )}
            </div>
          )}
          {!challenge && (page === 'register' || method === 'password') && (
            <PasswordField
              label='Password'
              name='password'
              required
              autoComplete={
                page === 'register' ? 'new-password' : 'current-password'
              }
              minLength={page === 'register' ? 8 : undefined}
              maxLength={72}
              value={password}
              disabled={busy}
              onChange={(event) => setPassword(event.target.value)}
            />
          )}
          {!challenge && page === 'register' && (
            <PasswordField
              label='Confirm password'
              name='confirmation'
              required
              autoComplete='new-password'
              maxLength={72}
              value={confirmation}
              disabled={busy}
              onChange={(event) => setConfirmation(event.target.value)}
            />
          )}
          <ErrorNotice>{error}</ErrorNotice>
          <button
            className='button primary full'
            type='submit'
            disabled={busy || sending || challengeExpired}
          >
            {busy
              ? 'Please wait…'
              : challenge
                ? 'Verify and sign in'
                : page === 'register'
                  ? 'Create account'
                  : 'Sign in'}
          </button>
        </form>
        {(challenge || page === 'register') && (
          <div className='auth-footer'>
            {challenge ? (
              <button
                className='text-button'
                disabled={busy || sending}
                onClick={() => reset('login')}
              >
                Back to sign in
              </button>
            ) : page === 'register' ? (
              <>
                Already have an account?{' '}
                <button
                  className='text-button'
                  disabled={busy || sending}
                  onClick={() => reset('login')}
                >
                  Sign in
                </button>
              </>
            ) : null}
          </div>
        )}
      </section>
    </main>
  );
}
