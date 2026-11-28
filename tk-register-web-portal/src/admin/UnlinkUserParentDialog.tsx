import { Unlink, X } from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { APIError, displayError, request, type DisplayError } from '../api';
import { t } from '../i18n';
import { ErrorNotice } from '../ui';
import type { AdminUser } from './types';

export default function UnlinkUserParentDialog({ user, onClose, onSaved, onRefresh }: {
  user: AdminUser; onClose: () => void; onSaved: (user: AdminUser) => void; onRefresh: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null), cancel = useRef<HTMLButtonElement>(null);
  const mounted = useRef(true), inFlight = useRef(false);
  const [saving, setSaving] = useState(false), [changed, setChanged] = useState(false);
  const [error, setError] = useState<DisplayError>('');
  useEffect(() => {
    mounted.current = true;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const overflow = document.body.style.overflow, element = dialog.current;
    document.body.style.overflow = 'hidden'; element?.showModal(); cancel.current?.focus();
    return () => { mounted.current = false; element?.close(); document.body.style.overflow = overflow; previous?.focus(); };
  }, []);
  const close = () => { if (!inFlight.current) onClose(); };
  async function save(event: FormEvent) {
    event.preventDefault();
    if (inFlight.current || !user.parent || user.membership_version === undefined) return;
    if (changed) { onRefresh(); return; }
    inFlight.current = true; setSaving(true); setError('');
    try {
      const result = await request<{ user: AdminUser }>('/admin/users/unlink-parent', {
        method: 'POST', body: { user_id: user.id, parent_id: user.parent.id, membership_version: user.membership_version },
      });
      if (mounted.current) onSaved(result.user);
    } catch (cause) {
      if (mounted.current) { setError(displayError(cause)); setChanged(cause instanceof APIError && cause.code === 'USER_PARENT_CHANGED'); }
    } finally { inFlight.current = false; if (mounted.current) setSaving(false); }
  }
  return <dialog ref={dialog} className='admin-create-dialog admin-parent-dialog' aria-labelledby='admin-parent-title' aria-describedby='admin-parent-description' onCancel={event => { event.preventDefault(); close(); }}>
    <header className='admin-create-heading'><div><h2 id='admin-parent-title'>{t('Unlink direct parent')}</h2><p id='admin-parent-description'>{t('Confirm the user and their current direct parent.')}</p></div><button type='button' className='admin-dialog-close' aria-label={t('Close dialog')} disabled={saving} onClick={close}><X aria-hidden='true' /></button></header>
    <form onSubmit={save} aria-busy={saving}>
      <div className='admin-create-fields'>
        <dl className='admin-parent-pair'>
          <div><dt>{t('User')}</dt><dd><strong>{user.email}</strong><span>{t('User #')}{user.id}</span></dd></div>
          <div><dt>{t('Direct parent')}</dt><dd><strong>{user.parent?.email}</strong><span>{t('User #')}{user.parent?.id}</span></dd></div>
        </dl>
        <div className='admin-parent-impact'><h3>{t('After unlinking')}</h3><p>{t('This user will become independent. Their former parent and its ancestors will lose access through this relationship to this user’s accounts and their descendants’ accounts.')}</p><p>{t('This user can still sign in. Their accounts, bills, submission history, and relationships with their own members will be kept.')}</p></div>
        <ErrorNotice>{error}</ErrorNotice>
      </div>
      <footer className='admin-create-actions'><button ref={cancel} type='button' className='button secondary' disabled={saving} onClick={close}>{t('Cancel')}</button><button type='submit' className={`button ${changed ? 'secondary' : 'admin-parent-confirm'}`} disabled={saving || !user.parent || user.membership_version === undefined}><Unlink aria-hidden='true' />{saving ? t('Unlinking…') : changed ? t('Refresh list') : t('Confirm unlink')}</button></footer>
    </form>
  </dialog>;
}
