// Workspace layout: tabs in up to two panes, persisted per environment.

export type TabKind = "terminal" | "desktop" | "log";

export interface Tab {
  key: string;
  kind: TabKind;
  title: string;
  /** Terminal session id (persistent in the environment). */
  session?: string;
  /** Process id for log tabs. */
  process?: string;
}

export interface Pane {
  tabs: Tab[];
  active?: string;
}

export interface WorkspaceLayout {
  panes: Pane[]; // 1 or 2
  split: "horizontal" | "vertical";
  focused: number;
  side?: SidePanel;
}

export type SidePanel = "activity" | "processes" | "ports" | "status" | "connect";

const key = (env: string) => `devenv.ws.${env}`;

export function loadLayout(env: string): WorkspaceLayout | null {
  try {
    const v = JSON.parse(localStorage.getItem(key(env)) ?? "null") as WorkspaceLayout | null;
    if (!v || !Array.isArray(v.panes) || v.panes.length === 0) return null;
    return v;
  } catch {
    return null;
  }
}

export function saveLayout(env: string, l: WorkspaceLayout) {
  localStorage.setItem(key(env), JSON.stringify(l));
}

let seq = 0;
export function newKey(): string {
  seq++;
  return `${Date.now().toString(36)}-${seq}`;
}

export function allTabs(l: WorkspaceLayout): Tab[] {
  return l.panes.flatMap((p) => p.tabs);
}

/** Immutable helpers. */
export function addTab(l: WorkspaceLayout, tab: Tab, pane = l.focused): WorkspaceLayout {
  const panes = l.panes.map((p, i) => (i === pane ? { tabs: [...p.tabs, tab], active: tab.key } : p));
  return { ...l, panes, focused: pane };
}

export function closeTab(l: WorkspaceLayout, tabKey: string): WorkspaceLayout {
  let panes = l.panes.map((p) => {
    const idx = p.tabs.findIndex((t) => t.key === tabKey);
    if (idx < 0) return p;
    const tabs = p.tabs.filter((t) => t.key !== tabKey);
    const active = p.active === tabKey ? tabs[Math.max(0, idx - 1)]?.key : p.active;
    return { tabs, active };
  });
  // Collapse an empty second pane.
  if (panes.length > 1) panes = panes.filter((p) => p.tabs.length > 0);
  if (panes.length === 0) panes = [{ tabs: [] }];
  return { ...l, panes, focused: Math.min(l.focused, panes.length - 1) };
}

export function updateTab(l: WorkspaceLayout, tabKey: string, patch: Partial<Tab>): WorkspaceLayout {
  return { ...l, panes: l.panes.map((p) => ({ ...p, tabs: p.tabs.map((t) => (t.key === tabKey ? { ...t, ...patch } : t)) })) };
}

export function activate(l: WorkspaceLayout, tabKey: string): WorkspaceLayout {
  const pane = l.panes.findIndex((p) => p.tabs.some((t) => t.key === tabKey));
  if (pane < 0) return l;
  return { ...l, focused: pane, panes: l.panes.map((p, i) => (i === pane ? { ...p, active: tabKey } : p)) };
}

/** Moves a tab to the other pane, creating the split when needed. */
export function moveToOtherPane(l: WorkspaceLayout, tabKey: string, split?: "horizontal" | "vertical"): WorkspaceLayout {
  const from = l.panes.findIndex((p) => p.tabs.some((t) => t.key === tabKey));
  if (from < 0) return l;
  const tab = l.panes[from].tabs.find((t) => t.key === tabKey)!;
  const to = from === 0 ? 1 : 0;
  let panes = l.panes.slice();
  if (panes.length === 1) panes.push({ tabs: [] });
  panes = panes.map((p, i) => {
    if (i === from) {
      const tabs = p.tabs.filter((t) => t.key !== tabKey);
      return { tabs, active: p.active === tabKey ? tabs[0]?.key : p.active };
    }
    if (i === to) return { tabs: [...p.tabs, tab], active: tab.key };
    return p;
  });
  panes = panes.filter((p) => p.tabs.length > 0);
  return { ...l, panes, split: split ?? l.split, focused: Math.min(panes.findIndex((p) => p.tabs.some((t) => t.key === tabKey)), panes.length - 1) };
}
