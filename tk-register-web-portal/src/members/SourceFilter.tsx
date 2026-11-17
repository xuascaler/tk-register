import { ChevronDown, Search } from 'lucide-react';
import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { selectedSources, sourceSummary, useMemberScope } from './MemberScope';

export default function SourceFilter({ id, onChange, searchable = false }: { id: string; onChange?: (selection: string) => void; searchable?: boolean }) {
  const scope = useMemberScope();
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const allBox = useRef<HTMLInputElement>(null);
  const searchInput = useRef<HTMLInputElement>(null);
  const [search, setSearch] = useState('');
  const [position, setPosition] = useState<{ top?: number; bottom?: number; left: number; width: number; maxHeight: number } | null>(null);
  const positioned = position !== null;
  const selected = selectedSources(scope.selection, scope.sources);
  const all = scope.sources.length > 0 && selected.length === scope.sources.length;
  const title = sourceSummary(scope.selection, scope.sources);
  const visibleSources = scope.sources.filter(source => !searchable || source.label.toLowerCase().includes(search.trim().toLowerCase()));
  function close(restore = false) { setOpen(false); setSearch(''); if (restore) trigger.current?.focus(); }
  function update(ids: string[]) {
    const selection = ids.length === scope.sources.length ? 'all' : ids.length ? ids.join(',') : 'none';
    scope.setSelection(selection);
    onChange?.(selection);
  }
  useLayoutEffect(() => { if (allBox.current) allBox.current.indeterminate = selected.length > 0 && !all; }, [all, selected.length, open, positioned]);
  useLayoutEffect(() => { if (open && positioned) (searchable ? searchInput.current : allBox.current)?.focus({ preventScroll: true }); }, [open, positioned, searchable]);
  useLayoutEffect(() => {
    if (!open) return;
    function place(event?: Event) {
      if (event?.target instanceof Node && menu.current?.contains(event.target)) return;
      const rect = trigger.current?.getBoundingClientRect(); if (!rect) return;
      const viewport = window.visualViewport;
      const vh = viewport?.height || window.innerHeight, vy = viewport?.offsetTop || 0;
      const vw = viewport?.width || window.innerWidth, vx = viewport?.offsetLeft || 0;
      if (rect.bottom < vy || rect.top > vy + vh) { close(); return; }
      const below = vy + vh - rect.bottom - 12, above = rect.top - vy - 12;
      const up = below < 260 && above > below;
      const width = Math.min(Math.max(rect.width, 280), vw - 24);
      setPosition({ ...(up ? { bottom: window.innerHeight - rect.top + 6 } : { top: rect.bottom + 6 }), left: Math.max(vx + 12, Math.min(rect.left, vx + vw - width - 12)), width, maxHeight: Math.max(100, Math.min(380, up ? above : below)) });
    }
    place();
    window.addEventListener('resize', place); window.addEventListener('scroll', place, true);
    window.visualViewport?.addEventListener('resize', place);
    return () => { window.removeEventListener('resize', place); window.removeEventListener('scroll', place, true); window.visualViewport?.removeEventListener('resize', place); };
  }, [open]);
  useEffect(() => {
    if (!open) return;
    const outside = (event: PointerEvent) => { if (event.target instanceof Node && !menu.current?.contains(event.target) && !trigger.current?.contains(event.target)) close(); };
    const focus = (event: FocusEvent) => { if (event.target instanceof Node && !menu.current?.contains(event.target) && !trigger.current?.contains(event.target)) close(); };
    const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') { event.preventDefault(); close(true); } };
    document.addEventListener('pointerdown', outside); document.addEventListener('focusin', focus); document.addEventListener('keydown', escape);
    return () => { document.removeEventListener('pointerdown', outside); document.removeEventListener('focusin', focus); document.removeEventListener('keydown', escape); };
  }, [open]);
  return <div className='member-source-filter tk-select'>
    <label id={`${id}-label`} htmlFor={id}>Source</label>
    <button ref={trigger} id={id} className='tk-select-trigger' type='button' aria-haspopup='dialog' aria-expanded={open} aria-controls={open ? `${id}-choices` : undefined} disabled={scope.loading || !!scope.error} onClick={() => setOpen(value => !value)} onKeyDown={event => { if (event.key === 'ArrowDown') { event.preventDefault(); setOpen(true); } }}><span>{scope.loading ? 'Loading sources…' : title}</span><ChevronDown aria-hidden='true' /></button>
    {scope.error && <button type='button' className='text-button member-source-retry' onClick={scope.reload}>Retry loading sources</button>}
    {open && position && createPortal(<div ref={menu} id={`${id}-choices`} className='member-source-menu' style={position} role='dialog' aria-labelledby={`${id}-label`}>
      {searchable && <div className='member-source-search'><Search aria-hidden='true' /><input ref={searchInput} type='search' aria-label='Search users' placeholder='Search user email' value={search} onChange={event => setSearch(event.target.value)} /></div>}
      <label className='member-source-choice member-source-all'><input ref={allBox} type='checkbox' checked={all} onChange={() => update(all ? [] : scope.sources.map(source => source.id))} /><span>Select all</span></label>
      <div className='member-source-options'>{visibleSources.map(source => <label className={`member-source-choice ${selected.includes(source.id) ? 'is-selected' : ''}`} key={source.id}><input type='checkbox' checked={selected.includes(source.id)} onChange={() => update(selected.includes(source.id) ? selected.filter(id => id !== source.id) : [...selected, source.id])} /><span>{source.label}</span></label>)}{!visibleSources.length && <p className='member-source-empty' role='status'>No matching users.</p>}</div>
      <footer><button type='button' className='button pale' onClick={() => close(true)}>Done</button></footer>
    </div>, document.body)}
  </div>;
}
