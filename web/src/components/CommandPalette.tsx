import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useNavigate } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { Layers, LayoutGrid, Monitor, Play, Plus, Settings, Square, SquareTerminal, Ticket, KeyRound } from "lucide-react";
import { api } from "../lib/api";
import { PhaseDot, cx } from "./ui";

export interface Command {
  id: string;
  title: string;
  hint?: string;
  icon?: ReactNode;
  group: string;
  run: () => void;
}

// Pages can register extra commands (the workspace adds its actions).
const extra = new Map<string, Command[]>();
const extraListeners = new Set<() => void>();

export function useRegisterCommands(key: string, cmds: Command[]) {
  useEffect(() => {
    extra.set(key, cmds);
    extraListeners.forEach((l) => l());
    return () => {
      extra.delete(key);
      extraListeners.forEach((l) => l());
    };
  }, [key, cmds]);
}

export function useCommandPalette() {
  const [open, setOpen] = useState(false);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen((o) => !o);
      }
    };
    const onOpen = () => setOpen(true);
    window.addEventListener("keydown", onKey);
    window.addEventListener("devenv:palette", onOpen);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("devenv:palette", onOpen);
    };
  }, []);
  return { open, setOpen };
}

export function CommandPalette({ open, setOpen }: { open: boolean; setOpen: (o: boolean) => void }) {
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const [, bump] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const envs = useQuery({ queryKey: ["environments", {}], queryFn: () => api.environments(), enabled: open });

  useEffect(() => {
    const l = () => bump((n) => n + 1);
    extraListeners.add(l);
    return () => {
      extraListeners.delete(l);
    };
  }, []);
  useEffect(() => {
    if (open) {
      setQ("");
      setSel(0);
      setTimeout(() => input.current?.focus(), 10);
    }
  }, [open]);

  const go = useCallback(
    (path: string) => () => {
      navigate(path);
    },
    [navigate],
  );

  const commands = useMemo<Command[]>(() => {
    const out: Command[] = [];
    for (const list of extra.values()) out.push(...list);
    for (const e of envs.data ?? []) {
      out.push({
        id: `env-${e.name}`,
        title: e.name,
        hint: `${e.image} · ${e.phase}`,
        icon: <PhaseDot phase={e.phase} />,
        group: "Environments",
        run: go(`/env/${e.name}`),
      });
      out.push({
        id: `env-term-${e.name}`,
        title: `${e.name}: new terminal`,
        icon: <SquareTerminal className="size-4" />,
        group: "Environments",
        run: go(`/env/${e.name}?open=terminal`),
      });
      if (e.desktop)
        out.push({
          id: `env-desk-${e.name}`,
          title: `${e.name}: desktop`,
          icon: <Monitor className="size-4" />,
          group: "Environments",
          run: go(`/env/${e.name}?open=desktop`),
        });
      out.push(
        e.running
          ? { id: `env-stop-${e.name}`, title: `${e.name}: stop`, icon: <Square className="size-4" />, group: "Environments", run: () => void api.stopEnvironment(e.name) }
          : { id: `env-start-${e.name}`, title: `${e.name}: start`, icon: <Play className="size-4" />, group: "Environments", run: () => void api.startEnvironment(e.name) },
      );
    }
    out.push(
      { id: "nav-new", title: "New environment", icon: <Plus className="size-4" />, group: "Go to", run: go("/new") },
      { id: "nav-envs", title: "Environments", icon: <LayoutGrid className="size-4" />, group: "Go to", run: go("/") },
      { id: "nav-images", title: "Images", icon: <Layers className="size-4" />, group: "Go to", run: go("/images") },
      { id: "nav-keys", title: "SSH keys", icon: <KeyRound className="size-4" />, group: "Go to", run: go("/settings/keys") },
      { id: "nav-tokens", title: "Access tokens", icon: <Ticket className="size-4" />, group: "Go to", run: go("/settings/tokens") },
      { id: "nav-prefs", title: "Preferences", icon: <Settings className="size-4" />, group: "Go to", run: go("/settings/preferences") },
    );
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [envs.data, go, open, extra.size]);

  const filtered = useMemo(() => {
    const terms = q.toLowerCase().split(/\s+/).filter(Boolean);
    return commands.filter((c) => terms.every((t) => `${c.title} ${c.hint ?? ""} ${c.group}`.toLowerCase().includes(t))).slice(0, 50);
  }, [commands, q]);

  if (!open) return null;
  const run = (c: Command) => {
    setOpen(false);
    c.run();
  };
  let lastGroup = "";
  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/40 pt-[12vh]" onClick={() => setOpen(false)}>
      <div className="w-full max-w-xl overflow-hidden rounded-box bg-base-100 shadow-2xl" onClick={(e) => e.stopPropagation()}>
        <input
          ref={input}
          className="input input-lg w-full rounded-none border-0 border-b border-base-300 focus:outline-none"
          placeholder="Type a command or environment…"
          value={q}
          onChange={(e) => {
            setQ(e.target.value);
            setSel(0);
          }}
          onKeyDown={(e) => {
            if (e.key === "Escape") setOpen(false);
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setSel((s) => Math.min(s + 1, filtered.length - 1));
            }
            if (e.key === "ArrowUp") {
              e.preventDefault();
              setSel((s) => Math.max(s - 1, 0));
            }
            if (e.key === "Enter" && filtered[sel]) run(filtered[sel]);
          }}
        />
        <ul className="max-h-[50vh] overflow-y-auto p-2">
          {filtered.length === 0 && <li className="p-4 text-sm text-base-content/60">No matches</li>}
          {filtered.map((c, i) => {
            const header = c.group !== lastGroup ? c.group : null;
            lastGroup = c.group;
            return (
              <li key={c.id}>
                {header && <div className="px-3 pt-2 pb-1 text-xs font-medium text-base-content/50">{header}</div>}
                <button
                  className={cx("flex w-full items-center gap-3 rounded-field px-3 py-2 text-left text-sm", i === sel && "bg-base-200")}
                  onMouseEnter={() => setSel(i)}
                  onClick={() => run(c)}
                >
                  <span className="flex w-4 justify-center">{c.icon}</span>
                  <span className="flex-1 truncate">{c.title}</span>
                  {c.hint && <span className="truncate text-xs text-base-content/50">{c.hint}</span>}
                </button>
              </li>
            );
          })}
        </ul>
      </div>
    </div>
  );
}
