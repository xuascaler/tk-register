import { Check, Copy, Info, X } from 'lucide-react';
import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';

// Portaled so horizontal table scrolling never clips the detail or changes row height.
export function ErrorPopover({ label, title, message, detail, note, className = '' }: {
  label: string; title: string; message: string; detail?: string; note?: string; className?: string;
}) {
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const pinned = useRef(false);
  const [open, setOpen] = useState(false);
  const [copyState, setCopyState] = useState('');
  const [position, setPosition] = useState({ top: 0, left: 0, ready: false });
  function cancelClose() { clearTimeout(timer.current); }
  function close(restoreFocus = false) { cancelClose(); pinned.current = false; setOpen(false); if (restoreFocus) trigger.current?.focus(); }
  function show() {
    cancelClose();
    window.dispatchEvent(new CustomEvent('account-detail-open', { detail: id }));
    setOpen(true);
  }
  function leave() { cancelClose(); if (!pinned.current) timer.current = setTimeout(() => setOpen(false), 180); }
  useEffect(() => () => clearTimeout(timer.current), []);
  useEffect(() => {
    function another(event: Event) { if ((event as CustomEvent).detail !== id) close(); }
    window.addEventListener('account-detail-open', another);
    return () => window.removeEventListener('account-detail-open', another);
  }, [id]);
  useLayoutEffect(() => {
    if (!open) { setPosition(v => ({ ...v, ready: false })); return; }
    setCopyState('');
    function place() {
      const button = trigger.current, popup = panel.current;
      if (!button || !popup) return;
      const r = button.getBoundingClientRect(), box = popup.getBoundingClientRect();
      if (r.bottom < 0 || r.top > innerHeight || r.right < 0 || r.left > innerWidth) { close(); return; }
      const top = r.bottom + 8 + box.height <= innerHeight - 12 ? r.bottom + 8 : Math.max(12, r.top - box.height - 8);
      setPosition({ top, left: Math.max(12, Math.min(r.left, innerWidth - box.width - 12)), ready: true });
    }
    place();
    const observer = new ResizeObserver(place); if (panel.current) observer.observe(panel.current);
    window.addEventListener('resize', place); window.addEventListener('scroll', place, true);
    function outside(event: PointerEvent) { if (!trigger.current?.contains(event.target as Node) && !panel.current?.contains(event.target as Node)) close(); }
    function escape(event: KeyboardEvent) { if (event.key === 'Escape') { event.stopPropagation(); close(true); } }
    document.addEventListener('pointerdown', outside); document.addEventListener('keydown', escape);
    return () => { observer.disconnect(); window.removeEventListener('resize', place); window.removeEventListener('scroll', place, true); document.removeEventListener('pointerdown', outside); document.removeEventListener('keydown', escape); };
  }, [open]);
  function blur(event: React.FocusEvent) {
    if (!trigger.current?.contains(event.relatedTarget as Node) && !panel.current?.contains(event.relatedTarget as Node)) close();
  }
  async function copy() {
    try { await navigator.clipboard.writeText([title, message, detail, note].filter(Boolean).join('\n\n')); setCopyState('Copied'); }
    catch { setCopyState('Could not copy. Select the text to copy it.'); }
  }
  return <>
    <button ref={trigger} type='button' className={`account-detail-trigger ${className}`} aria-label={`${label} details`} aria-expanded={open} aria-haspopup='dialog' aria-controls={open ? id : undefined}
      onMouseEnter={show} onMouseLeave={leave} onBlur={blur}
      onClick={() => { if (open && pinned.current) close(); else { pinned.current = true; show(); } }}>
      <span>{label}</span><Info aria-hidden='true' />
    </button>
    {open && createPortal(<div ref={panel} id={id} role='dialog' aria-modal='false' aria-labelledby={`${id}-title`} className='account-error-popover' style={{ top: position.top, left: position.left, visibility: position.ready ? 'visible' : 'hidden' }} onMouseEnter={cancelClose} onMouseLeave={leave} onFocus={() => { pinned.current = true; cancelClose(); }} onBlur={blur}>
      <div className='account-error-heading'><strong id={`${id}-title`}>{title}</strong><button type='button' aria-label='Close details' onClick={() => close(true)}><X aria-hidden='true' /></button></div>
      <p className='account-error-message'>{message}</p>
      {detail && <p className='account-error-meta'>{detail}</p>}
      {note && <p className='account-error-note'>{note}</p>}
      <div className='account-error-footer'><span role='status'>{copyState}</span><button type='button' onClick={copy}>{copyState === 'Copied' ? <Check aria-hidden='true' /> : <Copy aria-hidden='true' />}Copy details</button></div>
    </div>, document.body)}
  </>;
}
