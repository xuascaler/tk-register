import { ChevronDown, Search } from 'lucide-react';
import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import type { ModelSelection } from './daily-usage';

export function ModelFilter({ models, value, onChange }: { models: string[]; value: ModelSelection; onChange: (value: ModelSelection) => void }) {
  const [open, setOpen] = useState(false), [search, setSearch] = useState('');
  const trigger = useRef<HTMLButtonElement>(null), menu = useRef<HTMLDivElement>(null), input = useRef<HTMLInputElement>(null), allBox = useRef<HTMLInputElement>(null);
  const [position, setPosition] = useState<{ top?: number; bottom?: number; left: number; width: number; maxHeight: number } | null>(null);
  const selected = value === null ? models : models.filter(model => value.includes(model));
  const all = selected.length === models.length;
  function close(restore = false) { setOpen(false); if (restore) trigger.current?.focus(); }
  useLayoutEffect(() => { if (allBox.current) allBox.current.indeterminate = selected.length > 0 && !all; }, [selected.length, all, open, position]);
  useLayoutEffect(() => { if (open && position) input.current?.focus({ preventScroll: true }); }, [open, !!position]);
  useLayoutEffect(() => {
    if (!open) return;
    function place(event?: Event) {
      if (event?.target instanceof Node && menu.current?.contains(event.target)) return;
      const rect = trigger.current?.getBoundingClientRect(); if (!rect) return;
      const height = window.visualViewport?.height || window.innerHeight, width = window.visualViewport?.width || window.innerWidth;
      const up = height - rect.bottom < 300 && rect.top > height - rect.bottom;
      setPosition({ ...(up ? { bottom: window.innerHeight - rect.top + 6 } : { top: rect.bottom + 6 }), left: Math.max(12, Math.min(rect.left, width - 332)), width: Math.min(320, width - 24), maxHeight: Math.max(140, Math.min(400, up ? rect.top - 20 : height - rect.bottom - 20)) });
    }
    place(); window.addEventListener('resize', place); window.addEventListener('scroll', place, true);
    return () => { window.removeEventListener('resize', place); window.removeEventListener('scroll', place, true); };
  }, [open]);
  useEffect(() => {
    if (!open) return;
    const outside = (event: Event) => { if (event.target instanceof Node && !menu.current?.contains(event.target) && !trigger.current?.contains(event.target)) close(); };
    const key = (event: KeyboardEvent) => { if (event.key === 'Escape') { event.preventDefault(); close(true); } };
    document.addEventListener('pointerdown', outside); document.addEventListener('focusin', outside); document.addEventListener('keydown', key);
    return () => { document.removeEventListener('pointerdown', outside); document.removeEventListener('focusin', outside); document.removeEventListener('keydown', key); };
  }, [open]);
  return <div className='daily-model-filter tk-select'><label id='daily-model-label' htmlFor='daily-model-trigger'>Models</label>
    <button ref={trigger} id='daily-model-trigger' className='tk-select-trigger' type='button' disabled={!models.length} aria-expanded={open} aria-haspopup='dialog' aria-controls={open ? 'daily-model-menu' : undefined} onClick={() => { setSearch(''); setOpen(v => !v); }} onKeyDown={e => { if (e.key === 'ArrowDown') { e.preventDefault(); setOpen(true); } }}><span>{value === null || all ? 'All models' : selected.length ? 'Selected models' : 'Select models'}</span><ChevronDown aria-hidden='true' /></button>
    {open && position && createPortal(<div ref={menu} id='daily-model-menu' className='member-source-menu daily-model-menu' role='dialog' aria-labelledby='daily-model-label' style={position}>
      <label className='daily-model-search'><Search aria-hidden='true' /><input ref={input} type='search' aria-label='Search models' placeholder='Search models' value={search} onChange={e => setSearch(e.target.value)} /></label>
      <label className='member-source-choice member-source-all'><input ref={allBox} type='checkbox' checked={all} onChange={() => onChange(all ? [] : null)} /><span>Select all</span></label>
      <div className='member-source-options'>{models.filter(model => model.toLowerCase().includes(search.trim().toLowerCase())).map(model => <label className={`member-source-choice ${selected.includes(model) ? 'is-selected' : ''}`} key={model}><input type='checkbox' checked={selected.includes(model)} onChange={() => { const next = selected.includes(model) ? selected.filter(v => v !== model) : [...selected, model]; onChange(next.length === models.length ? null : next); }} /><span>{model}</span></label>)}{!models.some(model => model.toLowerCase().includes(search.trim().toLowerCase())) && <p className='usage-small daily-no-models'>No matching models</p>}</div>
      <footer><button type='button' className='text-button' onClick={() => onChange([])}>Clear selection</button><button type='button' className='button pale' onClick={() => close(true)}>Done</button></footer>
    </div>, document.body)}
  </div>;
}
