import { t, currentLanguage } from '../i18n';
import { ChevronLeft, ChevronRight } from 'lucide-react';
import { useEffect, useId, useState, type FormEvent } from 'react';

type Props = { page: number; total: number; pageSize: number; loading: boolean; scope: string; onPage: (page: number) => void };

export function AccountPagination({ page, total, pageSize, loading, scope, onPage }: Props) {
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const inputID = useId();
  const errorID = `${inputID}-error`;
  const [draft, setDraft] = useState(String(page));
  const [invalidPage, setInvalidPage] = useState(false);
  useEffect(() => { setDraft(String(page)); setInvalidPage(false); }, [page, total, pageSize, scope]);

  function change(next: number) {
    if (loading) return;
    setInvalidPage(false); setDraft(String(next));
    if (next !== page) onPage(next);
  }
  function jump(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (loading) return;
    const text = draft.trim(), next = Number(text);
    if (!/^\d+$/.test(text) || !Number.isSafeInteger(next) || next < 1 || next > totalPages) {
      setInvalidPage(true);
      return;
    }
    change(next);
  }

  return <div className='usage-pagination'>
    <span className='usage-pagination-summary' aria-live='polite'>{total.toLocaleString(currentLanguage())} {t(total === 1 ? 'account' : 'accounts')} · <span>{t('Page {{page}} of {{pages}}', { page, pages: totalPages })}</span></span>
    <nav className='usage-pagination-controls' aria-label={t('Account list pagination')}>
      <div className='usage-pagination-steps'>
        <button className='button pale' type='button' disabled={loading || page <= 1} onClick={() => change(page - 1)}><ChevronLeft aria-hidden='true' />{t('Previous')}</button>
        <button className='button pale' type='button' disabled={loading || page >= totalPages} onClick={() => change(page + 1)}>{t('Next')}<ChevronRight aria-hidden='true' /></button>
      </div>
      {totalPages > 1 && <form className='usage-page-jump' onSubmit={jump} noValidate>
        <label htmlFor={inputID}>{t('Go to')}</label>
        <input id={inputID} type='text' inputMode='numeric' autoComplete='off' spellCheck={false} maxLength={20} aria-label={t('Go to page')} aria-invalid={invalidPage ? true : undefined} aria-describedby={invalidPage ? errorID : undefined} value={draft} disabled={loading} onChange={event => { setDraft(event.target.value); setInvalidPage(false); }} />
        <button className='button pale' type='submit' disabled={loading}>{t('Go')}</button>
      </form>}
    </nav>
    {invalidPage && <p className='usage-page-jump-error' id={errorID} role='alert'>{t('Enter a page from 1 to {{pages}}.', { pages: totalPages.toLocaleString(currentLanguage()) })}</p>}
  </div>;
}
