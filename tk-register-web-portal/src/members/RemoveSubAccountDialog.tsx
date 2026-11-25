import { ChevronLeft, ChevronRight, Search, ShieldCheck, Unlink, UserRound, X } from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { APIError, displayError, request, type DisplayError } from '../api';
import { currentLanguage, t } from '../i18n';
import { ErrorNotice, Loading } from '../ui';
import type { Member, MemberPage } from './types';

export default function RemoveSubAccountDialog({ onClose, onRemoved, onRefresh }: {
  onClose: () => void;
  onRemoved: (user: { id: string; email: string }) => void;
  onRefresh: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null), input = useRef<HTMLInputElement>(null), confirm = useRef<HTMLButtonElement>(null);
  const mounted = useRef(true), inFlight = useRef(false);
  const [search, setSearch] = useState(''), [query, setQuery] = useState('');
  const [page, setPage] = useState(1), [revision, setRevision] = useState(0);
  const [data, setData] = useState<MemberPage | null>(null);
  const [selected, setSelected] = useState<Member | null>(null);
  const [loading, setLoading] = useState(true), [saving, setSaving] = useState(false), [changed, setChanged] = useState(false);
  const [loadError, setLoadError] = useState<DisplayError>(''), [error, setError] = useState<DisplayError>('');

  useEffect(() => {
    mounted.current = true;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const overflow = document.body.style.overflow, element = dialog.current;
    document.body.style.overflow = 'hidden'; element?.showModal(); input.current?.focus();
    return () => { mounted.current = false; element?.close(); document.body.style.overflow = overflow; previous?.focus(); };
  }, []);
  useEffect(() => { const timer = window.setTimeout(() => { setQuery(search.trim()); setPage(1); }, 300); return () => window.clearTimeout(timer); }, [search]);
  useEffect(() => {
    const abort = new AbortController(); setLoading(true); setLoadError('');
    request<MemberPage>(`/members?${new URLSearchParams({ page: String(page), search: query })}`, { signal: abort.signal })
      .then(result => { if (!abort.signal.aborted) setData(result); })
      .catch(cause => { if (!abort.signal.aborted) setLoadError(displayError(cause)); })
      .finally(() => { if (!abort.signal.aborted) setLoading(false); });
    return () => abort.abort();
  }, [page, query, revision]);
  useEffect(() => { if (selected) confirm.current?.focus(); else input.current?.focus(); }, [selected]);
  function close() { if (!inFlight.current) onClose(); }
  function refresh() {
    setChanged(false); setError(''); setSelected(null); setSearch(''); setQuery(''); setPage(1); setRevision(value => value + 1); onRefresh();
  }
  async function remove(event: FormEvent) {
    event.preventDefault();
    if (inFlight.current || !selected) return;
    if (changed) { refresh(); return; }
    inFlight.current = true; setSaving(true); setError('');
    try {
      const result = await request<{ user: { id: string; email: string } }>('/members/unlink', {
        method: 'POST', body: { member_id: selected.id, membership_version: selected.membership_version },
      });
      if (mounted.current) onRemoved(result.user);
    } catch (cause) {
      if (mounted.current) { setError(displayError(cause)); setChanged(cause instanceof APIError && cause.code === 'MEMBER_RELATION_CHANGED'); }
    } finally { inFlight.current = false; if (mounted.current) setSaving(false); }
  }
  const pages = data ? Math.max(1, Math.ceil(data.total / data.page_size)) : 1;
  return <dialog ref={dialog} className='admin-create-dialog member-link-dialog member-remove-dialog' aria-labelledby='member-remove-title' aria-describedby='member-remove-description' onCancel={event => { event.preventDefault(); close(); }}>
    <header className='admin-create-heading'><div><h2 id='member-remove-title'>{t('Del sub account')}</h2><p id='member-remove-description'>{t('Choose the sub account you want to disconnect.')}</p></div><button type='button' className='admin-dialog-close' disabled={saving} aria-label={t('Close dialog')} onClick={close}><X aria-hidden='true' /></button></header>
    <form onSubmit={remove} aria-busy={saving}>
      <div className='admin-create-fields member-remove-fields'>
        <div className='field'><label htmlFor={selected ? undefined : 'member-remove-search'}>{t('Sub account')}</label>
          {selected ? <div className='member-remove-selected'><span className='member-remove-avatar'><UserRound aria-hidden='true' /></span><div><strong>{selected.email}</strong><span>{t('Direct member')}</span></div><button type='button' className='admin-dialog-close' disabled={saving} aria-label={t('Choose another sub account')} onClick={() => { setSelected(null); setChanged(false); setError(''); }}><X aria-hidden='true' /></button></div> : <>
            <div className='member-remove-picker' aria-busy={loading}>
              <label className='usage-search'><Search aria-hidden='true' /><span className='sr-only'>{t('Search direct member email')}</span><input ref={input} id='member-remove-search' type='search' maxLength={50} autoComplete='off' value={search} placeholder={t('Search direct member email')} onChange={event => setSearch(event.target.value)} /></label>
              {loading ? <Loading label={t('Loading members…')} /> : loadError ? <div className='member-remove-empty'><ErrorNotice>{loadError}</ErrorNotice><button type='button' className='button pale' onClick={() => setRevision(value => value + 1)}>{t('Try again')}</button></div> : data?.items.length ? <div className='member-remove-options' role='group' aria-label={t('Direct members')}>{data.items.map(member => <label className='member-remove-option' key={member.id}><input type='radio' name='member-remove-choice' checked={false} onChange={() => { setSelected(member); setError(''); }} /><span><strong>{member.email}</strong><small>{t('Direct member')} · {t('{{count}} visible accounts', { count: member.visible_accounts.toLocaleString(currentLanguage()) })}</small></span></label>)}</div> : <p className='member-remove-empty'>{query ? t('No matching members') : t('No members yet')}</p>}
              {!loading && !loadError && pages > 1 && <div className='member-remove-pagination'><span>{t('Page {{page}} of {{pages}}', { page, pages })}</span><div><button type='button' className='button pale' disabled={page <= 1} aria-label={t('Previous')} onClick={() => setPage(value => value - 1)}><ChevronLeft aria-hidden='true' /></button><button type='button' className='button pale' disabled={page >= pages} aria-label={t('Next')} onClick={() => setPage(value => value + 1)}><ChevronRight aria-hidden='true' /></button></div></div>}
            </div><p className='admin-create-help'>{t('Choose one of your direct members.')}</p>
          </>}
        </div>
        {selected && <div className='member-remove-impact'><h3>{t('After removal')}</h3><div><Unlink aria-hidden='true' /><p>{t('You’ll no longer have access through this member to their accounts or their members’ accounts.')}</p></div><div><ShieldCheck aria-hidden='true' /><p>{t('This user can still sign in. Their accounts, data, and member relationships will be kept.')}</p></div></div>}
        <ErrorNotice>{error}</ErrorNotice>
      </div>
      <footer className='admin-create-actions'><button type='button' className='button secondary' disabled={saving} onClick={close}>{t('Cancel')}</button><button ref={confirm} type='submit' className={`button ${changed ? 'pale' : 'member-remove-confirm'}`} disabled={saving || !selected}><Unlink aria-hidden='true' />{saving ? t('Removing…') : changed ? t('Refresh members') : t('Confirm removal')}</button></footer>
    </form>
  </dialog>;
}
