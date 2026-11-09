import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { cx } from "./ui";

/**
 * A dropdown menu rendered in a portal with fixed positioning, so it is not
 * clipped by scroll containers (tables, tab bars). It opens below the trigger
 * (or above when there is no room), aligned to its right edge by default,
 * and closes on outside click, Escape, scroll or resize.
 */
export function Menu({
  trigger,
  children,
  align = "end",
  className,
  triggerClassName,
  label,
}: {
  trigger: ReactNode;
  /** Items; call close() after an action. */
  children: (close: () => void) => ReactNode;
  align?: "start" | "end";
  className?: string;
  triggerClassName?: string;
  label?: string;
}) {
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);
  const btn = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLUListElement>(null);
  const close = () => setOpen(false);

  useLayoutEffect(() => {
    if (!open || !btn.current || !menu.current) return;
    const b = btn.current.getBoundingClientRect();
    const m = menu.current.getBoundingClientRect();
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    let left = align === "end" ? b.right - m.width : b.left;
    left = Math.max(8, Math.min(left, vw - m.width - 8));
    let top = b.bottom + 4;
    if (top + m.height > vh - 8 && b.top - m.height - 4 > 8) top = b.top - m.height - 4;
    setPos({ top, left });
  }, [open, align]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: Event) => {
      const t = e.target as Node;
      if (!menu.current?.contains(t) && !btn.current?.contains(t)) close();
    };
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && close();
    const onMove = (e: Event) => {
      if (!menu.current?.contains(e.target as Node)) close();
    };
    document.addEventListener("pointerdown", onDown, true);
    document.addEventListener("keydown", onKey);
    window.addEventListener("scroll", onMove, true);
    window.addEventListener("resize", close);
    return () => {
      document.removeEventListener("pointerdown", onDown, true);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("scroll", onMove, true);
      window.removeEventListener("resize", close);
    };
  }, [open]);

  return (
    <>
      <button
        ref={btn}
        type="button"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        className={triggerClassName}
        onClick={(e) => {
          e.stopPropagation();
          setPos(null);
          setOpen((o) => !o);
        }}
      >
        {trigger}
      </button>
      {open &&
        createPortal(
          <ul
            ref={menu}
            role="menu"
            className={cx("menu fixed z-[60] rounded-box bg-base-100 p-1.5 shadow-lg ring-1 ring-base-300", className)}
            style={pos ? { top: pos.top, left: pos.left } : { top: -9999, left: -9999 }}
            onClick={(e) => e.stopPropagation()}
          >
            {children(close)}
          </ul>,
          document.body,
        )}
    </>
  );
}
