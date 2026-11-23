import { t } from '../i18n';
import { Check, Plus, ShieldPlus, ShieldCheck, UserMinus, UserPlus, X } from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { errorMessage, request, type User } from '../api';
import { dateTime, Empty, StateBadge } from '../submissions/shared';
import { ErrorNotice, Loading } from '../ui';
import { ListToolbar, Pagination, useListFilters } from './shared';
import type { AdminUser, Page } from './types';
import CreateUserDialog from './CreateUserDialog';

type RoleAction = 'admin' | 'member' | 'super_admin';

function RoleDialog({ role, currentUser, onClose, onSaved }: {
  role: RoleAction; currentUser: User; onClose: () => void; onSaved: (user: AdminUser) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const emailInput = useRef<HTMLInputElement>(null);
  const [email, setEmail] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);
  const mounted = useRef(true);
  const promoting = role === 'super_admin';
  const adding = role !== 'member';
  const label = t(promoting ? 'Add super admin' : adding ? 'Add admin' : 'Del admin');
  useEffect(() => {
    mounted.current = true;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    dialog.current?.showModal();
    emailInput.current?.focus();
    return () => {
      mounted.current = false;
      dialog.current?.close();
      document.body.style.overflow = overflow;
      previous?.focus();
    };
  }, []);
  const close = () => { if (!submitting.current) onClose(); };
  async function save(event: FormEvent) {
    event.preventDefault();
    if (submitting.current) return;
    const normalized = email.trim().toLowerCase();
    setEmail(normalized); setError('');
    if (!normalized || normalized.length > 50 || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(normalized)) {
      setError('Enter a valid email address, up to 50 characters.');
      emailInput.current?.focus(); return;
    }
    if (!adding && normalized === currentUser.email.toLowerCase()) {
      setError('You cannot remove your own admin access.');
      emailInput.current?.focus(); return;
    }
    submitting.current = true; setBusy(true);
    try {
      const result = await request<{ user: AdminUser }>('/admin/users/role', { method: 'POST', body: { email: normalized, role } });
      if (mounted.current) onSaved(result.user);
    } catch (cause) {
      if (mounted.current) { setError(errorMessage(cause)); emailInput.current?.focus(); }
    } finally {
      submitting.current = false;
      if (mounted.current) setBusy(false);
    }
  }
  return <dialog ref={dialog} className='admin-role-dialog' aria-labelledby='admin-role-title' aria-describedby='admin-role-description' onCancel={event => { event.preventDefault(); close(); }}>
    <div className='admin-dialog-heading'><span className='admin-dialog-icon'><ShieldCheck aria-hidden='true' /></span><h2 id='admin-role-title'>{label}</h2><button className='admin-dialog-close' type='button' aria-label={t("Close dialog")} disabled={busy} onClick={close}><X aria-hidden='true' /></button></div>
    <p id='admin-role-description' className='muted'>{promoting ? t("Enter an existing administrator’s email to grant super admin access.") : adding ? t("Enter an existing user's email to grant admin access.") : t("Enter an admin's email to remove admin access.")}</p>
    <form onSubmit={save} noValidate>
      <label className='admin-email-label' htmlFor='admin-role-email'>{t("Email address")}</label>
      <input ref={emailInput} id='admin-role-email' type='email' inputMode='email' autoComplete='off' autoCapitalize='none' spellCheck={false} maxLength={50} value={email} disabled={busy} aria-invalid={!!error} aria-describedby='admin-role-error admin-role-note' placeholder='name@example.com' onChange={event => { setEmail(event.target.value); setError(''); }} />
      <div id='admin-role-error'><ErrorNotice>{error}</ErrorNotice></div>
      <p className='admin-role-note' id='admin-role-note'>{promoting ? t("Super admins can view Raw account, edit model usage settings, and grant super admin access.") : adding ? t("Admins can manage system configuration and view all submitted accounts. Their next sign-in requires a password and email code.") : t("This user will become a member and keep access to their own accounts. They will need to sign in again.")}</p>
      <div className='admin-dialog-actions'><button type='button' className='button secondary' onClick={close} disabled={busy}>{t("Cancel")}</button><button type='submit' className='button primary' disabled={busy}>{busy ? t("Saving…") : label}</button></div>
    </form>
  </dialog>;
}

export default function Users({ currentUser, registrationEnabled }: { currentUser: User; registrationEnabled: boolean }) {
  const filters = useListFilters();
  const { page, query, attempt } = filters;
  const [list, setList] = useState<Page<AdminUser> | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState<{ message: string; email: string } | null>(null);
  const [role, setRole] = useState<RoleAction | null>(null);
  const [creating, setCreating] = useState(false);
  const [createdId, setCreatedId] = useState<string | null>(null);
  const registrationDeadlines = useRef<Record<string, number>>({});
  useEffect(() => {
    const abort = new AbortController();
    setLoading(true); setError('');
    const params = new URLSearchParams({ page: String(page), search: query });
    request<Page<AdminUser>>(`/admin/users?${params}`, { signal: abort.signal })
      .then(data => { if (!abort.signal.aborted) setList(data); })
      .catch(cause => { if (!abort.signal.aborted) setError(errorMessage(cause)); })
      .finally(() => { if (!abort.signal.aborted) setLoading(false); });
    return () => abort.abort();
  }, [page, query, attempt]);
  function saved(user: AdminUser) {
    setRole(null);
    setList(current => current && { ...current, items: current.items.map(item => String(item.id) === String(user.id) ? user : item) });
    setNotice(user.is_super_admin ? { message: "{{email}} is now a super admin.", email: user.email } : user.role === 'admin' ? { message: "{{email}} is now an admin.", email: user.email } : { message: "Admin access removed for {{email}}.", email: user.email });
    filters.refresh();
  }
  function created(user: AdminUser) {
    setCreating(false);
    setCreatedId(user.id);
    setNotice({ message: "Account created for {{email}}.", email: user.email });
    filters.resetSearch();
    filters.refresh();
  }
  return <div className='admin-users'>
    <div className='section-heading admin-users-heading'><div><h2>{t("Users")}</h2><p className='muted small'>{t("Create accounts and manage administrator access.")}</p></div>
      <div className='admin-role-actions'><button className='button primary admin-create-trigger' disabled={!registrationEnabled} aria-describedby={!registrationEnabled ? 'admin-registration-disabled' : undefined} onClick={() => { setNotice(null); setCreating(true); }}><Plus aria-hidden='true' />{t("Create account")}</button><button className='button secondary' onClick={() => { setNotice(null); setRole('admin'); }}><UserPlus aria-hidden='true' />{t("Add admin")}</button>{currentUser.is_super_admin && <button className='button secondary' onClick={() => { setNotice(null); setRole('super_admin'); }}><ShieldPlus aria-hidden='true' />{t("Add super admin")}</button>}<button className='button secondary' onClick={() => { setNotice(null); setRole('member'); }}><UserMinus aria-hidden='true' />{t("Del admin")}</button></div>
    </div>
    {!registrationEnabled && <p className='muted small' id='admin-registration-disabled'>{t("Registration is disabled. Enable it in Config → Access to create an account.")}</p>}
    {notice && <div className='notice success' role='status'><Check aria-hidden='true' />{t(notice.message, { email: notice.email })}</div>}
    <ListToolbar id='users' search={filters.search} onSearch={filters.setSearch} placeholder={t("Search user email")} busy={loading} onRefresh={filters.refresh} />
    <ErrorNotice>{error}</ErrorNotice>
    {loading && !list ? <Loading label={t("Loading users…")} /> : list ? <section className='panel admin-table-panel' aria-busy={loading}>
      {!list.items.length ? <Empty title={t("No matching users")}>{t("Try another email address.")}</Empty> : <div className='admin-table-wrap'><table className='admin-table admin-users-table'>
        <caption className='sr-only'>{t("Registered users and their roles")}</caption>
        <thead><tr><th scope='col'>{t("User")}</th><th scope='col'>{t("Role")}</th><th scope='col'>{t("Status")}</th><th scope='col'>{t("Joined")}</th></tr></thead>
        <tbody>{list.items.map(user => <tr key={user.id} className={user.id === createdId ? 'admin-user-created' : undefined}>
          <td className='admin-primary-cell' data-label={t("User")}><span className='admin-email'>{user.email}</span><span className='admin-cell-note'>{t("User #")}{user.id}{String(user.id) === String(currentUser.id) ? t(" · You") : user.id === createdId ? t(" · Just created") : ''}</span></td>
          <td data-label={t("Role")}><span className={`admin-role-badge ${user.role === 'admin' ? 'is-admin' : ''}`}>{user.role === 'admin' && <ShieldCheck aria-hidden='true' />}{user.is_super_admin ? t("Super admin") : user.role === 'admin' ? t("Admin") : t("Member")}</span></td>
          <td data-label={t("Status")}><StateBadge value={user.status} /></td>
          <td data-label={t("Joined")}><time dateTime={user.created_at}>{dateTime(user.created_at)}</time></td>
        </tr>)}</tbody>
      </table></div>}
      <Pagination data={list} noun='user' busy={loading} onPage={filters.setPage} />
    </section> : error ? <button className='button secondary' onClick={filters.refresh}>{t("Try again")}</button> : null}
    {role && <RoleDialog role={role} currentUser={currentUser} onClose={() => setRole(null)} onSaved={saved} />}
    {creating && <CreateUserDialog deadlines={registrationDeadlines} onClose={() => setCreating(false)} onSaved={created} />}
  </div>;
}
