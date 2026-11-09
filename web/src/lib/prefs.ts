import { useCallback, useEffect, useSyncExternalStore } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Me } from "./api";

// Console preferences are stored on the user (DevUser.spec.preferences) so
// they follow the user across browsers; localStorage keeps them available
// before /me loads.

export type Theme = "system" | "light" | "dark";

interface Prefs {
  theme: Theme;
  terminalFontSize: number;
  terminalFontFamily: string;
  terminalTheme: "dark" | "light" | "auto";
  terminalScrollback: number;
  defaultImage?: string;
  defaultClass?: string;
  /** Environment list view. */
  listView: "cards" | "table";
}

const defaultPrefs: Prefs = {
  theme: "system",
  terminalFontSize: 13,
  terminalFontFamily: 'ui-monospace, SFMono-Regular, Menlo, "JetBrains Mono", "DejaVu Sans Mono", monospace',
  terminalTheme: "auto",
  terminalScrollback: 10000,
  listView: "cards",
};

const KEY = "devenv.prefs";

function load(): Prefs {
  try {
    return { ...defaultPrefs, ...JSON.parse(localStorage.getItem(KEY) ?? "{}") };
  } catch {
    return defaultPrefs;
  }
}

let current = load();
const listeners = new Set<() => void>();

function set(p: Prefs) {
  current = p;
  localStorage.setItem(KEY, JSON.stringify(p));
  applyTheme(p.theme);
  listeners.forEach((l) => l());
}

function applyTheme(t: Theme) {
  const dark = t === "dark" || (t === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.dataset.theme = dark ? "devenv-dark" : "devenv-light";
}

applyTheme(current.theme);
window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => applyTheme(current.theme));

export function isDark(): boolean {
  return document.documentElement.dataset.theme === "devenv-dark";
}

export function usePrefs(): [Prefs, (patch: Partial<Prefs>) => void] {
  const qc = useQueryClient();
  const prefs = useSyncExternalStore(
    (cb) => {
      listeners.add(cb);
      return () => listeners.delete(cb);
    },
    () => current,
  );
  const save = useMutation({
    mutationFn: (p: Prefs) => api.updateMe({ preferences: p }),
    onSuccess: (me) => qc.setQueryData(["me"], me),
  });
  const update = useCallback(
    (patch: Partial<Prefs>) => {
      const next = { ...current, ...patch };
      set(next);
      save.mutate(next);
    },
    [save],
  );
  return [prefs, update];
}

/** Adopts server-side preferences once /me is loaded. */
export function useSyncPrefs(me: Me | undefined) {
  useEffect(() => {
    if (!me?.preferences || typeof me.preferences !== "object") return;
    const server = me.preferences as Partial<Prefs>;
    if (Object.keys(server).length > 0) set({ ...defaultPrefs, ...server });
  }, [me]);
}

export function useMe() {
  return useQuery({ queryKey: ["me"], queryFn: api.me, staleTime: 60_000 });
}

export function useInfo() {
  return useQuery({ queryKey: ["info"], queryFn: api.info, staleTime: 5 * 60_000 });
}
