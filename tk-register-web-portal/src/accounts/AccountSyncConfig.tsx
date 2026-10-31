import { Check, Eye, EyeOff, LoaderCircle, PlugZap, RefreshCw } from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { errorMessage, request } from '../api';
import { ErrorNotice, Loading } from '../ui';
import Select from '../submissions/Select';
import { formatTime } from './helpers';
import type { SyncConfig } from './types';

type Draft = { enabled: boolean; interval_hours: string; timezone: string; website_username: string; website_password: string };
function draftFrom(config: SyncConfig): Draft {
  return { enabled: config.enabled, interval_hours: String(config.interval_hours), timezone: config.timezone, website_username: config.website_username, website_password: '' };
}

export default function AccountSyncConfig({ onDirty }: { onDirty: (value: boolean) => void }) {
  const [saved, setSaved] = useState<SyncConfig | null>(null);
  const [draft, setDraft] = useState<Draft>({ enabled: false, interval_hours: '2', timezone: 'Asia/Shanghai', website_username: '', website_password: '' });
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<'save' | 'test' | ''>('');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [testResult, setTestResult] = useState<{ ok: boolean; text: string } | null>(null);
  const [visible, setVisible] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const pending = useRef<AbortController | null>(null);
  const changed = !!saved && (draft.enabled !== saved.enabled || draft.interval_hours !== String(saved.interval_hours) || draft.timezone !== saved.timezone || draft.website_username !== saved.website_username || draft.website_password !== '');
  useEffect(() => { onDirty(changed); return () => onDirty(false); }, [changed, onDirty]);
  useEffect(() => () => pending.current?.abort(), []);
  useEffect(() => {
    const controller = new AbortController(); setLoading(true); setError('');
    request<SyncConfig>('/admin/account-sync', { signal: controller.signal }).then(config => {
      if (!controller.signal.aborted) { setSaved(config); setDraft(draftFrom(config)); }
    }).catch(cause => { if (!controller.signal.aborted) setError(errorMessage(cause)); }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [attempt]);
  function change<K extends keyof Draft>(key: K, value: Draft[K]) {
    setDraft(current => ({ ...current, [key]: value })); setNotice(''); setError('');
    if (key === 'website_username' || key === 'website_password') setTestResult(null);
  }
  function credentialsError() {
    if (!draft.website_username.trim()) return 'Enter the supplier website username.';
    if (!draft.website_password && (!saved?.password_configured || draft.website_username.trim() !== saved.website_username)) return 'Enter the password for this username.';
    return '';
  }
  async function testLogin() {
    if (busy) return;
    const problem = credentialsError();
    if (problem) { setTestResult({ ok: false, text: problem }); return; }
    const controller = new AbortController(); pending.current = controller;
    setBusy('test'); setTestResult(null); setError('');
    try {
      const result = await request<{ authenticated: boolean }>('/admin/account-sync/test-login', { method: 'POST', signal: controller.signal, body: { website_username: draft.website_username.trim(), ...(draft.website_password ? { website_password: draft.website_password } : {}) } });
      if (!controller.signal.aborted) setTestResult({ ok: result.authenticated, text: result.authenticated ? 'Login successful. Changes have not been saved.' : 'Login could not be verified.' });
    } catch (cause) { if (!controller.signal.aborted) setTestResult({ ok: false, text: errorMessage(cause) }); }
    finally { if (!controller.signal.aborted) setBusy(''); }
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    if (busy || !changed) return;
    const hours = Number(draft.interval_hours);
    const credentialsChanged = draft.website_username !== saved?.website_username || !!draft.website_password;
    const problem = !Number.isInteger(hours) || hours < 1 || hours > 168 ? 'Enter a whole number of hours from 1 to 168.' : (draft.enabled || credentialsChanged) ? credentialsError() : '';
    if (problem) { setError(problem); return; }
    const controller = new AbortController(); pending.current = controller;
    setBusy('save'); setError(''); setNotice('');
    try {
      const config = await request<SyncConfig>('/admin/account-sync', { method: 'PUT', signal: controller.signal, body: { enabled: draft.enabled, interval_hours: hours, timezone: draft.timezone, website_username: draft.website_username.trim(), ...(draft.website_password ? { website_password: draft.website_password } : {}) } });
      if (!controller.signal.aborted) { setSaved(config); setDraft(draftFrom(config)); setVisible(false); setTestResult(null); setNotice('Changes saved.'); }
    } catch (cause) { if (!controller.signal.aborted) setError(errorMessage(cause)); }
    finally { if (!controller.signal.aborted) setBusy(''); }
  }
  function row(key: string, control: ReactNode, description: string) {
    return <tr><th scope='row'><code id={`sync-key-${key}`}>{key}</code></th><td className='cell-value'>{control}</td><td className='cell-description' id={`sync-description-${key}`}>{description}</td></tr>;
  }
  if (!saved) return <><ErrorNotice>{error}</ErrorNotice>{loading ? <Loading label='Loading account sync configuration…' /> : <button className='button pale' onClick={() => setAttempt(v => v + 1)}>Try again</button>}</>;
  return <section className='account-sync-config'>
    <div className='usage-config-heading'><div><h2>Account sync</h2><p className='usage-small'>Collect account status, usage, and cumulative spend.</p></div><span className='usage-vendor-label'>TK Register 0</span></div>
    <form onSubmit={save} noValidate>
      <table className='config-table usage-config-table'><caption className='sr-only'>Account collection settings</caption><colgroup><col /><col /><col /></colgroup><thead><tr><th scope='col'>Key</th><th scope='col'>Value</th><th scope='col'>Description</th></tr></thead><tbody>
        {row('enabled', <label className='switch'><input type='checkbox' role='switch' aria-labelledby='sync-key-enabled' checked={draft.enabled} disabled={!!busy} onChange={event => change('enabled', event.target.checked)} /><span>{draft.enabled ? 'Enabled' : 'Paused'}</span></label>, 'Run collection automatically, even when no one has the website open.')}
        {row('interval_hours', <div className='usage-interval'><input id='sync-interval' type='number' min='1' max='168' step='1' value={draft.interval_hours} disabled={!!busy} aria-labelledby='sync-key-interval_hours' aria-describedby='sync-description-interval_hours' onChange={event => change('interval_hours', event.target.value)} /><span>hours</span></div>, 'Time between collections. A new interval applies after saving; existing history is kept.')}
        {row('timezone', <Select id='sync-timezone' label='Collection timezone' hiddenLabel value={draft.timezone} disabled={!!busy} onChange={value => change('timezone', value)} options={[{ value: 'Asia/Shanghai', label: 'Asia/Shanghai' }, { value: 'Asia/Singapore', label: 'Asia/Singapore' }, { value: 'UTC', label: 'UTC' }]} />, 'Timezone used for collection dates and usage history. Each snapshot retains its actual collection time.')}
        {row('data_source', <span>Supplier website</span>, 'Read account information from the supplier website.')}
        {row('website_username', <input id='sync-username' type='text' autoComplete='off' spellCheck={false} maxLength={254} value={draft.website_username} disabled={!!busy} aria-labelledby='sync-key-website_username' aria-describedby='sync-description-website_username' placeholder='Website login username' onChange={event => change('website_username', event.target.value)} />, 'The administrator’s login username for the supplier website.')}
        {row('website_password', <><div className='usage-password'><input id='sync-password' type={visible ? 'text' : 'password'} autoComplete='new-password' maxLength={4096} value={draft.website_password} disabled={!!busy} aria-labelledby='sync-key-website_password' aria-describedby='sync-password-hint sync-description-website_password' placeholder={saved.password_configured ? 'Enter a new password' : 'Website login password'} onChange={event => change('website_password', event.target.value)} /><button type='button' aria-label={visible ? 'Hide new password' : 'Show new password'} disabled={!!busy} onClick={() => setVisible(v => !v)}>{visible ? <EyeOff aria-hidden='true' /> : <Eye aria-hidden='true' />}</button></div><p className='field-help' id='sync-password-hint'>{saved.password_configured ? 'Password saved · Leave blank to keep it.' : 'No password saved.'}</p></>, 'Stored encrypted and never shown again. Enter a new password when changing the username.')}
      </tbody></table>
      <div className='usage-login-test'><button type='button' className='button pale' disabled={!!busy} onClick={testLogin}>{busy === 'test' ? <LoaderCircle className='usage-spinning' aria-hidden='true' /> : <PlugZap aria-hidden='true' />}{busy === 'test' ? 'Testing login…' : 'Test login'}</button><span className={`usage-test-result ${testResult ? testResult.ok ? 'positive' : 'negative' : ''}`} role='status'>{testResult?.text || 'Not tested'}</span></div>
      <p className='usage-small'>Test the current entries without saving or starting collection.</p>
      <p className='usage-policy'>The server signs in automatically before each collection. Saved credentials are used only by the server. If collection fails, keep the last successful values and their timestamp; accounts without data show “—”.</p>
      <ErrorNotice>{error}</ErrorNotice>
      {notice && <div className='notice success' role='status'><Check aria-hidden='true' />{notice}</div>}
      <div className='save-footer'><span className='save-state' role='status'>{changed ? 'Unsaved changes' : 'No unsaved changes'}</span><div className='footer-actions'><button type='button' className='button quiet' disabled={!changed || !!busy} onClick={() => { setDraft(draftFrom(saved)); setVisible(false); setError(''); setNotice(''); setTestResult(null); }}>Discard</button><button type='submit' className='button primary' disabled={!changed || !!busy}>{busy === 'save' ? 'Saving…' : 'Save changes'}</button></div></div>
    </form>
    <div className='usage-sync-status'><div className='usage-config-heading'><h2>Collection status</h2><button type='button' className='button pale usage-icon-button' aria-label='Refresh collection status' disabled={loading || !!busy || changed} onClick={() => setAttempt(v => v + 1)}><RefreshCw aria-hidden='true' /></button></div><dl><div><dt>Status</dt><dd>{saved.running ? 'Collecting' : saved.enabled ? 'Scheduled' : 'Paused'}</dd></div><div><dt>Last attempt</dt><dd>{formatTime(saved.last_run_at, saved.timezone)}</dd></div><div><dt>Last success</dt><dd>{formatTime(saved.last_success_at, saved.timezone)}</dd></div><div><dt>Next collection</dt><dd>{saved.enabled ? formatTime(saved.next_run_at, saved.timezone) : '—'}</dd></div></dl>{saved.last_error && <div className='usage-stale-notice'><strong>Last collection failed</strong><p>{saved.last_error}</p></div>}</div>
  </section>;
}
