import { Check, ChevronDown } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { createPortal } from "react-dom";
import "./Select.css";

export type SelectOption = { value: string; label: string };
export type SelectProps = {
  id: string;
  label: string;
  options: readonly SelectOption[];
  hiddenLabel?: boolean;
  name?: string;
  required?: boolean;
  invalid?: boolean;
  describedBy?: string;
  placeholder?: string;
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
};

export default function Select({
  id,
  label,
  options,
  value,
  onChange,
  disabled = false,
  hiddenLabel = false,
  name,
  required,
  invalid,
  describedBy,
  placeholder = "Select an option",
}: SelectProps) {
  const trigger = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const search = useRef({ text: "", time: 0 });
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const [position, setPosition] = useState<{
    top?: number;
    bottom?: number;
    left: number;
    width: number;
    maxHeight: number;
  } | null>(null);
  const selected = options.findIndex((option) => option.value === value);
  const expanded = open && !disabled && options.length > 0;
  const listId = `${id}-listbox`;
  const activeId = `${id}-option-${active}`;

  function close() {
    setOpen(false);
    search.current = { text: "", time: 0 };
  }

  function show(index = Math.max(0, selected)) {
    setActive(index);
    setOpen(true);
  }

  function choose(index: number) {
    const option = options[index];
    close();
    trigger.current?.focus({ preventScroll: true });
    if (option && option.value !== value) onChange(option.value);
  }

  useLayoutEffect(() => {
    if (!expanded) return;
    function place(event?: Event) {
      if (
        event?.type === "scroll" &&
        event.target instanceof Node &&
        list.current?.contains(event.target)
      )
        return;
      const rect = trigger.current?.getBoundingClientRect();
      if (!rect) return;
      const viewport = window.visualViewport;
      const top = viewport?.offsetTop ?? 0;
      const left = viewport?.offsetLeft ?? 0;
      const height = viewport?.height ?? window.innerHeight;
      const width = viewport?.width ?? window.innerWidth;
      if (rect.bottom <= top || rect.top >= top + height) {
        close();
        return;
      }
      const below = Math.max(0, top + height - rect.bottom - 14);
      const above = Math.max(0, rect.top - top - 14);
      const upward =
        below < Math.min(272, options.length * 44 + 8) && above > below;
      const menuWidth = Math.min(rect.width, width - 16);
      setPosition({
        ...(upward
          ? { bottom: window.innerHeight - rect.top + 6 }
          : { top: rect.bottom + 6 }),
        left: Math.max(
          left + 8,
          Math.min(rect.left, left + width - menuWidth - 8),
        ),
        width: menuWidth,
        maxHeight: Math.min(272, upward ? above : below),
      });
    }
    place();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    window.visualViewport?.addEventListener("resize", place);
    window.visualViewport?.addEventListener("scroll", place);
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
      window.visualViewport?.removeEventListener("resize", place);
      window.visualViewport?.removeEventListener("scroll", place);
    };
  }, [expanded, options.length]);

  useLayoutEffect(() => {
    if (!expanded || !position) return;
    const item = list.current?.children[active] as HTMLElement | undefined;
    const menu = list.current;
    if (!item || !menu) return;
    if (item.offsetTop < menu.scrollTop) menu.scrollTop = item.offsetTop;
    else if (
      item.offsetTop + item.offsetHeight >
      menu.scrollTop + menu.clientHeight
    )
      menu.scrollTop = item.offsetTop + item.offsetHeight - menu.clientHeight;
  }, [active, expanded, position]);

  useEffect(() => {
    if (!expanded) return;
    function outside(event: Event) {
      if (
        event.target instanceof Node &&
        !trigger.current?.contains(event.target) &&
        !list.current?.contains(event.target)
      )
        close();
    }
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("focusin", outside);
    };
  }, [expanded]);

  useEffect(() => {
    if (disabled || !options.length) setOpen(false);
  }, [disabled, options.length]);

  function handleKey(event: KeyboardEvent<HTMLButtonElement>) {
    if (disabled || !options.length) return;
    const last = options.length - 1;
    if (event.key === "Escape" || (event.altKey && event.key === "ArrowUp")) {
      if (expanded) {
        event.preventDefault();
        event.stopPropagation();
        close();
      }
      return;
    }
    if (event.key === "Tab") {
      close();
      return;
    }
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      if (expanded) choose(active);
      else show();
      return;
    }
    if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
      event.preventDefault();
      if (event.key === "Home") show(0);
      else if (event.key === "End") show(last);
      else if (!expanded) show();
      else
        setActive((current) =>
          Math.max(
            0,
            Math.min(last, current + (event.key === "ArrowDown" ? 1 : -1)),
          ),
        );
      search.current = { text: "", time: 0 };
      return;
    }
    if (
      event.key.length !== 1 ||
      event.ctrlKey ||
      event.metaKey ||
      event.altKey
    )
      return;
    event.preventDefault();
    const now = Date.now();
    const previous = now - search.current.time < 700 ? search.current.text : "";
    const text = (previous + event.key).toLocaleLowerCase();
    const prefix = [...text].every((character) => character === text[0])
      ? text[0]
      : text;
    const current = expanded ? active : selected;
    const start = prefix.length === 1 ? current + 1 : Math.max(0, current);
    const match = Array.from(
      { length: options.length },
      (_, i) => (start + i) % options.length,
    ).find((index) =>
      options[index].label.toLocaleLowerCase().startsWith(prefix),
    );
    search.current = { text, time: now };
    if (match !== undefined) show(match);
  }

  return (
    <div className="tk-select">
      <label
        id={`${id}-label`}
        htmlFor={id}
        className={hiddenLabel ? "sr-only" : undefined}
      >
        {label}
      </label>
      <button
        ref={trigger}
        id={id}
        name={name}
        type="button"
        role="combobox"
        className="tk-select-trigger"
        disabled={disabled || !options.length}
        aria-labelledby={`${id}-label`}
        aria-required={required || undefined}
        aria-invalid={invalid || undefined}
        aria-describedby={describedBy}
        aria-haspopup="listbox"
        aria-expanded={expanded}
        aria-controls={expanded ? listId : undefined}
        aria-activedescendant={
          expanded && options[active] ? activeId : undefined
        }
        onKeyDown={handleKey}
        onClick={() => (expanded ? close() : show())}
      >
        <span>{options[selected]?.label ?? placeholder}</span>
        <ChevronDown aria-hidden="true" />
      </button>
      {name && (
        <input type="hidden" name={name} value={value} disabled={disabled} />
      )}
      {expanded &&
        position &&
        createPortal(
          <div
            ref={list}
            id={listId}
            role="listbox"
            aria-labelledby={`${id}-label`}
            className="tk-select-menu"
            style={position}
            onMouseDown={(event) => event.preventDefault()}
          >
            {options.map((option, index) => (
              <div
                key={option.value}
                id={`${id}-option-${index}`}
                role="option"
                aria-selected={option.value === value}
                data-active={index === active || undefined}
                className="tk-select-option"
                onPointerMove={(event) => {
                  if (event.pointerType === "mouse") setActive(index);
                }}
                onClick={() => choose(index)}
              >
                <span>{option.label}</span>
                {option.value === value && <Check aria-hidden="true" />}
              </div>
            ))}
          </div>,
          document.body,
        )}
    </div>
  );
}
