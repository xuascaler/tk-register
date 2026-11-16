import { CalendarDays } from 'lucide-react';
import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { recentDailyRange } from './daily-usage';
import { validOverviewRange, type OverviewRangeSelection } from './overview';

type Range = { start: string; end: string };
const presets = [{ period: 'day', days: 1, label: 'Past day' }, { period: 'week', days: 7, label: 'Past 7 days' }, { period: 'month', days: 30, label: 'Past month' }];

export default function OverviewDateFilter({ range, allTime, period, onDays, onAllTime, onApply }: {
  range: Range; allTime: boolean; period?: OverviewRangeSelection; onDays: (days: number) => void; onAllTime: () => void; onApply: (range: Range) => void;
}) {
  const [open, setOpen] = useState(false), [draft, setDraft] = useState(range), [error, setError] = useState('');
  const trigger = useRef<HTMLButtonElement>(null), menu = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState<{ top: number; left: number; width: number; maxHeight: number } | null>(null);
  const allSelected = period === undefined ? allTime : period === 'all_time';
  const selected = !allSelected && presets.find(preset => { if (period !== undefined) return preset.period === period; const value = recentDailyRange(preset.days); return value.start === range.start && value.end === range.end; });
  const title = allSelected ? 'All time' : selected ? selected.label : `${range.start} – ${range.end}`;
  const currentPresetRange = selected ? recentDailyRange(selected.days) : null;
  const savedRangeDiffers = !!currentPresetRange && (currentPresetRange.start !== range.start || currentPresetRange.end !== range.end);
  function close(restore = false) { setOpen(false); if (restore) trigger.current?.focus(); }
  function show() { setDraft(range); setError(''); setPosition(null); setOpen(true); }
  useLayoutEffect(() => {
    if (!open) return;
    function place(event?: Event) {
      if (event?.target instanceof Node && menu.current?.contains(event.target)) return;
      const rect = trigger.current?.getBoundingClientRect(); if (!rect) return;
      const viewport = window.visualViewport, width = viewport?.width || window.innerWidth, height = viewport?.height || window.innerHeight;
      const vx = viewport?.offsetLeft || 0, vy = viewport?.offsetTop || 0;
      if (rect.bottom < vy || rect.top > vy + height) { close(); return; }
      const menuWidth = Math.min(380, width - 24), desiredHeight = width <= 400 || menuWidth < 340 ? 380 : 290;
      const below = vy + height - rect.bottom - 18, above = rect.top - vy - 18;
      const up = below < desiredHeight && above > below;
      const maxHeight = Math.max(100, Math.min(desiredHeight + 30, up ? above : below));
      setPosition({ top: up ? Math.max(vy + 12, rect.top - maxHeight - 6) : rect.bottom + 6, left: Math.max(vx + 12, Math.min(rect.left + (rect.width - menuWidth) / 2, vx + width - menuWidth - 12)), width: menuWidth, maxHeight });
    }
    place(); window.addEventListener('resize', place); window.addEventListener('scroll', place, true);
    window.visualViewport?.addEventListener('resize', place);
    return () => { window.removeEventListener('resize', place); window.removeEventListener('scroll', place, true); window.visualViewport?.removeEventListener('resize', place); };
  }, [open]);
  useLayoutEffect(() => { if (open && position) menu.current?.querySelector<HTMLButtonElement>('button')?.focus({ preventScroll: true }); }, [open, !!position]);
  useEffect(() => {
    if (!open) return;
    const outside = (event: Event) => { if (event.target instanceof Node && !menu.current?.contains(event.target) && !trigger.current?.contains(event.target)) close(); };
    const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') { event.preventDefault(); close(true); } };
    document.addEventListener('pointerdown', outside); document.addEventListener('focusin', outside); document.addEventListener('keydown', escape);
    return () => { document.removeEventListener('pointerdown', outside); document.removeEventListener('focusin', outside); document.removeEventListener('keydown', escape); };
  }, [open]);
  return <div className='overview-period tk-select'>
    <label htmlFor='overview-period-trigger'>Usage period</label>
    <button ref={trigger} id='overview-period-trigger' type='button' className='tk-select-trigger' aria-label={`Usage date range: ${title}`} aria-haspopup='dialog' aria-expanded={open} aria-controls={open ? 'overview-date-menu' : undefined} onClick={() => open ? close() : show()} onKeyDown={event => { if (event.key === 'ArrowDown') { event.preventDefault(); show(); } }}><span>{title}</span><CalendarDays aria-hidden='true' /></button>
    {savedRangeDiffers && <span className='overview-period-range'>{range.start} – {range.end}</span>}
    {open && position && createPortal(<div ref={menu} id='overview-date-menu' className='overview-date-menu' style={position} role='dialog' aria-label='Usage dates'>
      <div className='overview-presets' role='group' aria-label='Usage period'>
        {presets.map(preset => <button key={preset.days} type='button' aria-pressed={!!selected && selected.days === preset.days} onClick={() => { onDays(preset.days); close(true); }}>{preset.label}</button>)}
        <button type='button' aria-pressed={allSelected} onClick={() => { onAllTime(); close(true); }}>All time</button>
      </div>
      <form onSubmit={event => { event.preventDefault(); if (!validOverviewRange(draft.start, draft.end)) { setError('Choose valid start and end dates, ending no later than yesterday.'); return; } onApply(draft); close(true); }}>
        <div className='overview-dates'><label>Start date<input type='date' value={draft.start} min='1970-01-01' max={recentDailyRange(1).end} onChange={event => setDraft(value => ({ ...value, start: event.target.value }))} required /></label><label>End date<input type='date' value={draft.end} min='1970-01-01' max={recentDailyRange(1).end} onChange={event => setDraft(value => ({ ...value, end: event.target.value }))} required /></label></div>
        {error && <p className='overview-date-error' role='alert'>{error}</p>}
        <p className='overview-date-note'>Daily data through {recentDailyRange(1).end} · Asia/Shanghai</p>
        <div className='overview-date-actions'><button type='button' className='button secondary' onClick={() => close(true)}>Cancel</button><button className='button primary' type='submit'>Apply</button></div>
      </form>
    </div>, document.body)}
  </div>;
}
