import { ChevronLeft, ChevronRight } from 'lucide-react';
import { useEffect, useId, useState, type FormEvent } from 'react';

type Props = { page: number; total: number; pageSize: number; loading: boolean; scope: string; onPage: (page: number) => void };

export function AccountPagination({ page, total, pageSize, loading, scope, onPage }: Props) {
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const inputID = useId();
  const errorID = `${inputID}-error`;
  const [draft, setDraft] = useState(String(page));
  const [error, setError] = useState('');
  useEffect(() => { setDraft(String(page)); setError(''); }, [page, total, pageSize, scope]);

  function change(next: number) {
    if (loading) return;
    setError(''); setDraft(String(next));
    if (next !== page) onPage(next);
  }
  function jump(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (loading) return;
    const text = draft.trim(), next = Number(text);
    if (!/^\d+$/.test(text) || !Number.isSafeInteger(next) || next < 1 || next > totalPages) {
      setError(`Enter a page from 1 to ${totalPages.toLocaleString()}.`);
      return;
    }
    change(next);
  }

  return <div className='usage-pagination'>
    <span className='usage-pagination-summary' aria-live='polite'>{total.toLocaleString()} {total === 1 ? 'account' : 'accounts'} · <span>Page {page} of {totalPages}</span></span>
    <nav className='usage-pagination-controls' aria-label='Account list pagination'>
      <div className='usage-pagination-steps'>
        <button className='button pale' type='button' disabled={loading || page <= 1} onClick={() => change(page - 1)}><ChevronLeft aria-hidden='true' />Previous</button>
        <button className='button pale' type='button' disabled={loading || page >= totalPages} onClick={() => change(page + 1)}>Next<ChevronRight aria-hidden='true' /></button>
      </div>
      {totalPages > 1 && <form className='usage-page-jump' onSubmit={jump} noValidate>
        <label htmlFor={inputID}>Go to</label>
        <input id={inputID} type='text' inputMode='numeric' autoComplete='off' spellCheck={false} maxLength={20} aria-label='Go to page' aria-invalid={error ? true : undefined} aria-describedby={error ? errorID : undefined} value={draft} disabled={loading} onChange={event => { setDraft(event.target.value); setError(''); }} />
        <button className='button pale' type='submit' disabled={loading}>Go</button>
      </form>}
    </nav>
    {error && <p className='usage-page-jump-error' id={errorID} role='alert'>{error}</p>}
  </div>;
}
