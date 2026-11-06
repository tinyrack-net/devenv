import { useState, type ReactNode } from "react";
import type { Phase } from "../api";

export function PhaseBadge({ phase, deleting }: { phase: Phase; deleting?: boolean }) {
  const label = deleting ? "Deleting" : phase;
  const cls = deleting
    ? "warn"
    : { Running: "ok", Failed: "bad", Stopped: "idle", Pending: "warn", Starting: "warn", Stopping: "warn" }[phase];
  return <span className={`badge ${cls}`}>{label}</span>;
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  return <div className="alert">{error instanceof Error ? error.message : String(error)}</div>;
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className="small"
      onClick={async () => {
        await navigator.clipboard.writeText(text);
        setDone(true);
        setTimeout(() => setDone(false), 1500);
      }}
    >
      {done ? "Copied" : label}
    </button>
  );
}

export function Field({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <label className="field">
      <span className="field-label">{label}</span>
      {children}
      {hint && <span className="field-hint">{hint}</span>}
    </label>
  );
}

export function timeAgo(iso: string): string {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return `${Math.floor(s)}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

/** Phases that change on their own; poll faster while in them. */
export function isTransitional(phase: Phase, deleting?: boolean) {
  return deleting || phase === "Pending" || phase === "Starting" || phase === "Stopping";
}
