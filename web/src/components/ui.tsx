import { useEffect, useRef, useState, type ReactNode } from "react";
import { Check, Copy, AlertTriangle, Loader2, X } from "lucide-react";
import { errorMessage, isTransitional } from "../lib/api";

export function cx(...c: (string | false | null | undefined)[]): string {
  return c.filter(Boolean).join(" ");
}

const phaseClass: Record<string, string> = {
  Running: "badge-success",
  Starting: "badge-info",
  Pending: "badge-info",
  Stopping: "badge-warning",
  Stopped: "badge-ghost",
  Failed: "badge-error",
};

export function PhaseBadge({ phase, deleting, size = "sm" }: { phase: string; deleting?: boolean; size?: "xs" | "sm" | "md" }) {
  const label = deleting ? "Deleting" : phase;
  return (
    <span className={cx("badge gap-1 whitespace-nowrap", `badge-${size}`, deleting ? "badge-warning" : phaseClass[phase] ?? "badge-ghost")}>
      {isTransitional(phase, deleting) && <Loader2 className="size-3 animate-spin" />}
      {label}
    </span>
  );
}

/** Dot for compact phase display. */
export function PhaseDot({ phase }: { phase: string }) {
  const color =
    phase === "Running" ? "status-success" : phase === "Failed" ? "status-error" : phase === "Stopped" ? "" : "status-info";
  return <span className={cx("status", color, isTransitional(phase) && "animate-pulse")} />;
}

export function ErrorAlert({ error, className }: { error: unknown; className?: string }) {
  if (!error) return null;
  return (
    <div role="alert" className={cx("alert alert-error alert-soft", className)}>
      <AlertTriangle className="size-4" />
      <span className="break-words">{errorMessage(error)}</span>
    </div>
  );
}

export function Spinner({ className }: { className?: string }) {
  return <span className={cx("loading loading-spinner", className)} />;
}

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <div className="flex items-center gap-2 p-8 text-base-content/60">
      <Spinner className="loading-sm" /> {label}
    </div>
  );
}

export function Empty({ icon, title, children }: { icon?: ReactNode; title: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-box border border-dashed border-base-300 p-10 text-center">
      {icon && <div className="text-base-content/40">{icon}</div>}
      <div className="font-medium">{title}</div>
      {children && <div className="text-sm text-base-content/60">{children}</div>}
    </div>
  );
}

export function CopyButton({ text, label, className }: { text: string; label?: string; className?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className={cx("btn btn-ghost btn-xs", className)}
      title="Copy"
      onClick={async () => {
        await navigator.clipboard.writeText(text);
        setDone(true);
        setTimeout(() => setDone(false), 1200);
      }}
    >
      {done ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
      {label}
    </button>
  );
}

/** A monospace value with a copy button. */
export function CopyField({ value, className }: { value: string; className?: string }) {
  return (
    <div className={cx("flex items-start gap-1 rounded-field bg-base-200 px-3 py-2", className)}>
      <code className="font-mono-tight flex-1 break-all whitespace-pre-wrap">{value}</code>
      <CopyButton text={value} />
    </div>
  );
}

export function Field({ label, hint, children, className }: { label: ReactNode; hint?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <fieldset className={cx("fieldset", className)}>
      <legend className="fieldset-legend">{label}</legend>
      {children}
      {hint && <p className="label whitespace-normal">{hint}</p>}
    </fieldset>
  );
}

export function timeAgo(iso?: string | null): string {
  if (!iso) return "-";
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return `${Math.floor(s)}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export function formatTime(iso?: string | null): string {
  if (!iso) return "-";
  return new Date(iso).toLocaleString();
}

/** Modal dialog driven by `open`. */
export function Modal({
  open,
  onClose,
  title,
  children,
  actions,
  wide,
}: {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  children: ReactNode;
  actions?: ReactNode;
  wide?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);
  return (
    <dialog ref={ref} className="modal" onClose={onClose}>
      <div className={cx("modal-box", wide && "max-w-3xl")}>
        <button type="button" className="btn btn-sm btn-circle btn-ghost absolute top-3 right-3" onClick={onClose}>
          <X className="size-4" />
        </button>
        <h3 className="mb-4 text-lg font-semibold">{title}</h3>
        {open && children}
        {actions && <div className="modal-action">{actions}</div>}
      </div>
      <form method="dialog" className="modal-backdrop">
        <button>close</button>
      </form>
    </dialog>
  );
}

/** Confirmation dialog; for destructive actions require typing a name. */
export function Confirm({
  open,
  onClose,
  onConfirm,
  title,
  children,
  confirmLabel = "Confirm",
  danger,
  typeToConfirm,
  busy,
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  title: string;
  children?: ReactNode;
  confirmLabel?: string;
  danger?: boolean;
  typeToConfirm?: string;
  busy?: boolean;
}) {
  const [typed, setTyped] = useState("");
  useEffect(() => setTyped(""), [open]);
  const ok = !typeToConfirm || typed === typeToConfirm;
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      actions={
        <>
          <button className="btn btn-ghost" onClick={onClose}>
            Cancel
          </button>
          <button className={cx("btn", danger ? "btn-error" : "btn-primary")} disabled={!ok || busy} onClick={onConfirm}>
            {busy && <Spinner className="loading-xs" />}
            {confirmLabel}
          </button>
        </>
      }
    >
      <div className="space-y-3 text-sm">
        {children}
        {typeToConfirm && (
          <Field label={<>Type <code className="font-mono">{typeToConfirm}</code> to confirm</>}>
            <input className="input w-full" value={typed} onChange={(e) => setTyped(e.target.value)} autoFocus />
          </Field>
        )}
      </div>
    </Modal>
  );
}

/** Progress bar for used/limit, colored by fill. */
export function Meter({ value, label }: { value: number | undefined; label: string }) {
  if (value === undefined) return <span className="text-xs text-base-content/60">{label}</span>;
  const color = value > 0.9 ? "progress-error" : value > 0.7 ? "progress-warning" : "progress-primary";
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs text-base-content/70">{label}</span>
      <progress className={cx("progress h-1.5 w-full", color)} value={value * 100} max={100} />
    </div>
  );
}

/** Tag input for users / groups. */
export function TagInput({
  value,
  onChange,
  placeholder,
  suggestions = [],
}: {
  value: string[];
  onChange: (v: string[]) => void;
  placeholder?: string;
  suggestions?: { value: string; label?: string }[];
}) {
  const [text, setText] = useState("");
  const add = (v: string) => {
    const t = v.trim();
    if (t && !value.includes(t)) onChange([...value, t]);
    setText("");
  };
  const listId = useRef(`tags-${Math.random().toString(36).slice(2)}`).current;
  return (
    <div className="input flex h-auto min-h-10 w-full flex-wrap items-center gap-1 py-1.5">
      {value.map((v) => (
        <span key={v} className="badge badge-neutral gap-1">
          {v}
          <button type="button" onClick={() => onChange(value.filter((x) => x !== v))}>
            <X className="size-3" />
          </button>
        </span>
      ))}
      <input
        className="min-w-24 flex-1 bg-transparent outline-none"
        list={suggestions.length ? listId : undefined}
        value={text}
        placeholder={value.length ? "" : placeholder}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === "," || e.key === " ") {
            e.preventDefault();
            add(text);
          } else if (e.key === "Backspace" && !text && value.length) {
            onChange(value.slice(0, -1));
          }
        }}
        onBlur={() => text && add(text)}
      />
      {suggestions.length > 0 && (
        <datalist id={listId}>
          {suggestions
            .filter((s) => !value.includes(s.value))
            .map((s) => (
              <option key={s.value} value={s.value}>
                {s.label}
              </option>
            ))}
        </datalist>
      )}
    </div>
  );
}
