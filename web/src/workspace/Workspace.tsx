import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Group, Panel, Separator } from "react-resizable-panels";
import {
  ArrowLeft,
  Bot,
  Columns2,
  Gauge,
  Monitor,
  Network,
  Plug,
  Plus,
  Rows2,
  ScrollText,
  Search,
  Settings,
  Share2,
  SquareTerminal,
  X,
  Cpu,
  PanelRightClose,
} from "lucide-react";
import { api, ApiError, type Environment, type Process, type PtySession } from "../lib/api";
import { useInfo, useMe } from "../lib/prefs";
import { formatBytes } from "../lib/quantity";
import { useIsMobile } from "../lib/media";
import { useRegisterCommands, type Command } from "../components/CommandPalette";
import { ErrorAlert, Loading, PhaseBadge, Spinner, cx } from "../components/ui";
import { ThemeSwitch, UserMenu } from "../components/Layout";
import { Menu } from "../components/Menu";
import { EnvMenu, PowerButton } from "../components/EnvActions";
import { TerminalView } from "./TerminalView";
import { DesktopView } from "./DesktopView";
import { ActivityPanel, useRunningAgentCalls } from "./ActivityPanel";
import { ConnectPanel, PortsPanel, ProcessLog, ProcessesPanel, StatusPanel } from "./Panels";
import {
  activate,
  addTab,
  allTabs,
  closeTab,
  loadLayout,
  moveToOtherPane,
  newKey,
  saveLayout,
  updateTab,
  type SidePanel,
  type Tab,
  type WorkspaceLayout,
} from "./layout";

export function WorkspacePage() {
  const { name = "" } = useParams();
  const env = useQuery({ queryKey: ["environment", name], queryFn: () => api.environment(name), refetchInterval: (q) => (q.state.data?.phase === "Running" ? 30_000 : 4_000) });
  if (env.error instanceof ApiError && env.error.status === 404)
    return (
      <div className="flex h-screen flex-col items-center justify-center gap-3">
        <p>Environment “{name}” does not exist or is not shared with you.</p>
        <Link to="/" className="btn btn-primary btn-sm">
          Back to environments
        </Link>
      </div>
    );
  if (!env.data) return env.error ? <ErrorAlert error={env.error} className="m-6" /> : <Loading label="Opening workspace…" />;
  return <Workspace env={env.data} />;
}

function defaultLayout(env: Environment): WorkspaceLayout {
  const tabs: Tab[] = [{ key: newKey(), kind: "terminal", title: "Terminal" }];
  if (env.desktop) tabs.push({ key: newKey(), kind: "desktop", title: "Desktop" });
  return { panes: [{ tabs, active: env.desktop ? tabs[1].key : tabs[0].key }], split: "horizontal", focused: 0, side: env.createdBy === "mcp" ? "activity" : undefined };
}

const sidePanels: { id: SidePanel; label: string; icon: ReactNode }[] = [
  { id: "activity", label: "Agent activity", icon: <Bot className="size-4" /> },
  { id: "processes", label: "Processes", icon: <Cpu className="size-4" /> },
  { id: "ports", label: "Ports", icon: <Network className="size-4" /> },
  { id: "status", label: "Status", icon: <Gauge className="size-4" /> },
  { id: "connect", label: "Connect", icon: <Plug className="size-4" /> },
];

function Workspace({ env }: { env: Environment }) {
  const navigate = useNavigate();
  const mobile = useIsMobile();
  const qc = useQueryClient();
  const me = useMe();
  useInfo();
  const [params, setParams] = useSearchParams();
  const [layout, setLayout] = useState<WorkspaceLayout>(() => loadLayout(env.name) ?? defaultLayout(env));
  const ready = env.phase === "Running" && !env.deleting;
  const agentRunning = useRunningAgentCalls(env.name);
  const sessions = useQuery({ queryKey: ["sessions", env.name], queryFn: () => api.sessions(env.name), enabled: ready, refetchInterval: 15_000 });

  useEffect(() => saveLayout(env.name, layout), [env.name, layout]);
  useEffect(() => {
    document.title = `${env.name} · devenv`;
    return () => {
      document.title = "devenv";
    };
  }, [env.name]);

  const update = useCallback((fn: (l: WorkspaceLayout) => WorkspaceLayout) => setLayout((l) => fn(l)), []);
  const openTerminal = useCallback(
    (session?: PtySession, pane?: number) => {
      update((l) => {
        if (session) {
          const existing = allTabs(l).find((t) => t.session === session.id);
          if (existing) return activate(l, existing.key);
        }
        const n = allTabs(l).filter((t) => t.kind === "terminal").length + 1;
        return addTab(l, { key: newKey(), kind: "terminal", title: session?.name ?? (n > 1 ? `Terminal ${n}` : "Terminal"), session: session?.id }, pane);
      });
    },
    [update],
  );
  const openDesktop = useCallback(() => {
    update((l) => {
      const existing = allTabs(l).find((t) => t.kind === "desktop");
      return existing ? activate(l, existing.key) : addTab(l, { key: newKey(), kind: "desktop", title: "Desktop" });
    });
  }, [update]);
  const openLog = useCallback(
    (p: Process) => {
      update((l) => {
        const existing = allTabs(l).find((t) => t.kind === "log" && t.process === p.id);
        return existing ? activate(l, existing.key) : addTab(l, { key: newKey(), kind: "log", title: p.id, process: p.id });
      });
    },
    [update],
  );
  const setSide = (side?: SidePanel) => update((l) => ({ ...l, side: l.side === side ? undefined : side }));

  // ?open=terminal|desktop from links elsewhere in the console.
  useEffect(() => {
    const open = params.get("open");
    if (!open) return;
    if (open === "terminal") openTerminal();
    if (open === "desktop" && env.desktop) openDesktop();
    params.delete("open");
    setParams(params, { replace: true });
  }, [params, setParams, openTerminal, openDesktop, env.desktop]);

  // Keyboard shortcuts.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.ctrlKey || e.metaKey;
      if (mod && e.shiftKey && e.key.toLowerCase() === "t") {
        e.preventDefault();
        openTerminal();
      } else if (mod && e.shiftKey && e.key.toLowerCase() === "d" && env.desktop) {
        e.preventDefault();
        openDesktop();
      } else if (mod && e.altKey && /^[1-9]$/.test(e.key)) {
        const tabs = allTabs(layout);
        const t = tabs[Number(e.key) - 1];
        if (t) update((l) => activate(l, t.key));
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [layout, openTerminal, openDesktop, env.desktop, update]);

  const commands = useMemo<Command[]>(
    () => [
      { id: "ws-term", title: "New terminal", hint: "Ctrl+Shift+T", icon: <SquareTerminal className="size-4" />, group: env.name, run: () => openTerminal() },
      ...(env.desktop ? [{ id: "ws-desk", title: "Open desktop", hint: "Ctrl+Shift+D", icon: <Monitor className="size-4" />, group: env.name, run: openDesktop }] : []),
      ...sidePanels.map((p) => ({ id: `ws-side-${p.id}`, title: `Show ${p.label.toLowerCase()}`, icon: p.icon, group: env.name, run: () => setLayout((l) => ({ ...l, side: p.id })) })),
      { id: "ws-split", title: "Toggle split direction", icon: <Columns2 className="size-4" />, group: env.name, run: () => setLayout((l) => ({ ...l, split: l.split === "horizontal" ? "vertical" : "horizontal" })) },
      { id: "ws-settings", title: "Environment settings", icon: <Settings className="size-4" />, group: env.name, run: () => navigate(`/env/${env.name}/settings`) },
      { id: "ws-restart", title: "Restart environment", icon: <Plug className="size-4" />, group: env.name, run: () => void api.restartEnvironment(env.name) },
    ],
    [env.name, env.desktop, openTerminal, openDesktop, navigate],
  );
  useRegisterCommands("workspace", commands);

  // Sessions that exist in the environment but have no tab (opened by
  // someone else, or in another browser).
  const orphanSessions = (sessions.data ?? []).filter((s) => !allTabs(layout).some((t) => t.session === s.id));

  const renderTab = (tab: Tab, active: boolean) => {
    switch (tab.kind) {
      case "terminal":
        return (
          <TerminalView
            env={env.name}
            sessionId={tab.session}
            name={tab.title}
            active={active}
            ready={ready}
            onSession={(s) => {
              if (s.id !== tab.session) update((l) => updateTab(l, tab.key, { session: s.id }));
              qc.invalidateQueries({ queryKey: ["sessions", env.name] });
            }}
          />
        );
      case "desktop":
        return <DesktopView env={env.name} active={active} ready={ready} />;
      case "log":
        return tab.process ? <ProcessLog env={env.name} id={tab.process} active={active} /> : null;
    }
  };

  const closeTerminalTab = async (tab: Tab, kill: boolean) => {
    update((l) => closeTab(l, tab.key));
    if (kill && tab.session && ready) {
      try {
        await api.killSession(env.name, tab.session);
      } catch {
        /* already gone */
      }
      qc.invalidateQueries({ queryKey: ["sessions", env.name] });
    }
  };

  const paneView = (paneIdx: number) => {
    const pane = layout.panes[paneIdx];
    return (
      <div className={cx("flex h-full min-h-0 flex-col", layout.panes.length > 1 && layout.focused === paneIdx && "ring-1 ring-primary/40 ring-inset")} onMouseDown={() => layout.focused !== paneIdx && update((l) => ({ ...l, focused: paneIdx }))}>
        <div className="flex items-center gap-0.5 overflow-x-auto border-b border-base-300 bg-base-200 px-1">
          {pane.tabs.map((t) => (
            <div
              key={t.key}
              className={cx(
                "group relative flex shrink-0 cursor-pointer items-center gap-1.5 px-3 py-2 text-sm select-none",
                pane.active === t.key
                  ? "bg-base-100 font-medium after:absolute after:inset-x-2 after:-bottom-px after:h-0.5 after:rounded-full after:bg-primary"
                  : "text-base-content/60 hover:bg-base-100/50 hover:text-base-content",
              )}
              onClick={() => update((l) => activate(l, t.key))}
              onDoubleClick={() => {
                if (t.kind !== "terminal" || !t.session) return;
                const n = prompt("Rename terminal", t.title);
                if (n) {
                  update((l) => updateTab(l, t.key, { title: n }));
                  api.renameSession(env.name, t.session, n).catch(() => {});
                }
              }}
              onAuxClick={(e) => e.button === 1 && (t.kind === "terminal" ? closeTerminalTab(t, false) : update((l) => closeTab(l, t.key)))}
              title={t.kind === "terminal" ? "Double-click to rename. Closing the tab keeps the shell running." : undefined}
            >
              {t.kind === "terminal" ? <SquareTerminal className="size-3.5" /> : t.kind === "desktop" ? <Monitor className="size-3.5" /> : <ScrollText className="size-3.5" />}
              <span className="max-w-40 truncate">{t.title}</span>
              <Menu
                label={`Close ${t.title}`}
                align="start"
                triggerClassName="btn btn-ghost btn-xs btn-square opacity-0 group-hover:opacity-100 pointer-coarse:opacity-60 aria-expanded:opacity-100"
                className="w-60 text-xs font-normal"
                trigger={<X className="size-3" />}
              >
                {(close) =>
                  t.kind === "terminal" ? (
                    <>
                      <li>
                        <button onClick={() => { close(); closeTerminalTab(t, false); }}>Close tab (keep shell running)</button>
                      </li>
                      <li>
                        <button className="text-error" onClick={() => { close(); closeTerminalTab(t, true); }}>
                          Close and end the shell
                        </button>
                      </li>
                      <li>
                        <button onClick={() => { close(); update((l) => moveToOtherPane(l, t.key)); }}>Move to other pane</button>
                      </li>
                    </>
                  ) : (
                    <>
                      <li>
                        <button onClick={() => { close(); update((l) => closeTab(l, t.key)); }}>Close</button>
                      </li>
                      <li>
                        <button onClick={() => { close(); update((l) => moveToOtherPane(l, t.key)); }}>Move to other pane</button>
                      </li>
                    </>
                  )
                }
              </Menu>
            </div>
          ))}
          <Menu label="New tab" align="start" triggerClassName="btn btn-ghost btn-xs btn-square shrink-0" className="w-64 text-sm" trigger={<Plus className="size-3.5" />}>
            {(close) => (
              <>
                <li>
                  <button onClick={() => { close(); openTerminal(undefined, paneIdx); }}>
                    <SquareTerminal className="size-4" /> New terminal <kbd className="kbd kbd-xs ml-auto">⌃⇧T</kbd>
                  </button>
                </li>
                {env.desktop && (
                  <li>
                    <button onClick={() => { close(); openDesktop(); }}>
                      <Monitor className="size-4" /> Desktop <kbd className="kbd kbd-xs ml-auto">⌃⇧D</kbd>
                    </button>
                  </li>
                )}
                {orphanSessions.length > 0 && <li className="menu-title text-xs">Running shells</li>}
                {orphanSessions.map((s) => (
                  <li key={s.id}>
                    <button onClick={() => { close(); openTerminal(s, paneIdx); }}>
                      <SquareTerminal className="size-4" /> {s.name}
                      <span className="ml-auto text-xs text-base-content/50">{s.clients > 0 ? `${s.clients} viewing` : "detached"}</span>
                    </button>
                  </li>
                ))}
              </>
            )}
          </Menu>
        </div>
        <div className="relative min-h-0 flex-1">
          {pane.tabs.length === 0 && (
            <div className="flex h-full flex-col items-center justify-center gap-3 text-sm text-base-content/60">
              <div>No open tabs</div>
              <div className="flex gap-2">
                <button className="btn btn-sm" onClick={() => openTerminal(undefined, paneIdx)}>
                  <SquareTerminal className="size-4" /> Terminal
                </button>
                {env.desktop && (
                  <button className="btn btn-sm" onClick={openDesktop}>
                    <Monitor className="size-4" /> Desktop
                  </button>
                )}
              </div>
            </div>
          )}
          {pane.tabs.map((t) => (
            <div key={t.key} className={cx("absolute inset-0", pane.active !== t.key && "invisible")}>
              {renderTab(t, pane.active === t.key)}
            </div>
          ))}
          {!ready && pane.tabs.length > 0 && <NotReady env={env} />}
        </div>
      </div>
    );
  };

  const sideOpen = !!layout.side;
  const shared = env.sharing.users.length + env.sharing.groups.length > 0;
  const sideContent = (
    <>
      {layout.side === "activity" && <ActivityPanel env={env.name} />}
      {layout.side === "processes" && <ProcessesPanel env={env.name} ready={ready} onOpenLog={(p) => { openLog(p); if (mobile) setSide(undefined); }} />}
      {layout.side === "ports" && <PortsPanel env={env} ready={ready} />}
      {layout.side === "status" && <StatusPanel env={env} ready={ready} />}
      {layout.side === "connect" && <ConnectPanel env={env} />}
    </>
  );
  const sideHeader = (
    <div className="flex items-center gap-2 border-b border-base-300 px-3 py-2 text-sm font-medium">
      {sidePanels.find((p) => p.id === layout.side)?.icon}
      {sidePanels.find((p) => p.id === layout.side)?.label}
      <div className="flex-1" />
      <button className="btn btn-ghost btn-sm btn-square md:btn-xs" aria-label="Close panel" onClick={() => setSide(undefined)}>
        {mobile ? <X className="size-5" /> : <PanelRightClose className="size-4" />}
      </button>
    </div>
  );

  if (mobile) {
    // One pane, panels as a full screen sheet, navigation at the bottom.
    const pane0 = layout.panes.flatMap((p) => p.tabs);
    const activeKey = layout.panes[layout.focused]?.active ?? pane0[0]?.key;
    const activeTab = pane0.find((t) => t.key === activeKey) ?? pane0[0];
    return (
      <div className="h-dvh-safe flex flex-col bg-base-100">
        <header className="flex items-center gap-1 border-b border-base-300 px-1 py-1 pt-[max(0.25rem,env(safe-area-inset-top))]">
          <Link to="/" className="btn btn-ghost btn-sm btn-square" aria-label="All environments">
            <ArrowLeft className="size-5" />
          </Link>
          <span className="min-w-0 truncate font-semibold">{env.name}</span>
          <PhaseBadge phase={env.phase} deleting={env.deleting} size="xs" />
          {agentRunning > 0 && (
            <button className="badge badge-secondary badge-xs gap-1" onClick={() => setLayout((l) => ({ ...l, side: "activity" }))}>
              <Bot className="size-3" /> {agentRunning}
            </button>
          )}
          <div className="flex-1" />
          <PowerButton env={env} className="btn-xs" />
          <EnvMenu env={env} onDeleted={() => navigate("/")} />
        </header>
        <div className="flex items-center gap-1 overflow-x-auto border-b border-base-300 bg-base-200 px-1 py-1">
          {pane0.map((t) => (
            <div
              key={t.key}
              className={cx(
                "flex shrink-0 items-center gap-1 rounded-field py-1 pr-1 pl-2.5 text-sm",
                activeTab?.key === t.key ? "bg-base-100 font-medium shadow-sm" : "text-base-content/60",
              )}
              onClick={() => update((l) => activate(l, t.key))}
            >
              {t.kind === "terminal" ? <SquareTerminal className="size-3.5" /> : t.kind === "desktop" ? <Monitor className="size-3.5" /> : <ScrollText className="size-3.5" />}
              <span className="max-w-28 truncate">{t.title}</span>
              <button
                className="btn btn-ghost btn-xs btn-square"
                aria-label={`Close ${t.title}`}
                onClick={(e) => {
                  e.stopPropagation();
                  if (t.kind === "terminal") closeTerminalTab(t, false);
                  else update((l) => closeTab(l, t.key));
                }}
              >
                <X className="size-3.5" />
              </button>
            </div>
          ))}
          <button className="btn btn-ghost btn-sm btn-square shrink-0" aria-label="New terminal" onClick={() => openTerminal(undefined, 0)}>
            <Plus className="size-4" />
          </button>
          {env.desktop && !pane0.some((t) => t.kind === "desktop") && (
            <button className="btn btn-ghost btn-sm btn-square shrink-0" aria-label="Desktop" onClick={openDesktop}>
              <Monitor className="size-4" />
            </button>
          )}
          {orphanSessions.map((s) => (
            <button key={s.id} className="btn btn-ghost btn-xs shrink-0 text-base-content/60" onClick={() => openTerminal(s, 0)}>
              <SquareTerminal className="size-3" /> {s.name}
            </button>
          ))}
        </div>
        <div className="relative min-h-0 flex-1">
          {pane0.length === 0 && (
            <div className="flex h-full flex-col items-center justify-center gap-3 text-sm text-base-content/60">
              <button className="btn btn-sm" onClick={() => openTerminal(undefined, 0)}>
                <SquareTerminal className="size-4" /> Terminal
              </button>
              {env.desktop && (
                <button className="btn btn-sm" onClick={openDesktop}>
                  <Monitor className="size-4" /> Desktop
                </button>
              )}
            </div>
          )}
          {pane0.map((t) => (
            <div key={t.key} className={cx("absolute inset-0", activeTab?.key !== t.key && "invisible")}>
              {renderTab(t, activeTab?.key === t.key && !sideOpen)}
            </div>
          ))}
          {!ready && pane0.length > 0 && <NotReady env={env} />}
          {sideOpen && (
            <div className="absolute inset-0 z-30 flex flex-col bg-base-100">
              {sideHeader}
              <div className="min-h-0 flex-1 overflow-y-auto">{sideContent}</div>
            </div>
          )}
        </div>
        <nav className="flex items-stretch justify-around border-t border-base-300 bg-base-200 pb-[env(safe-area-inset-bottom)]">
          {sidePanels.map((p) => (
            <button
              key={p.id}
              className={cx("relative flex flex-1 flex-col items-center gap-0.5 py-1.5 text-[10px]", layout.side === p.id ? "text-primary" : "text-base-content/60")}
              onClick={() => setSide(p.id)}
            >
              {p.icon}
              {p.label.replace("Agent activity", "Agent")}
              {p.id === "activity" && agentRunning > 0 && <span className="badge badge-secondary badge-xs absolute top-0.5 right-1/4">{agentRunning}</span>}
            </button>
          ))}
          <Link to={`/env/${env.name}/settings`} className="flex flex-1 flex-col items-center gap-0.5 py-1.5 text-[10px] text-base-content/60">
            <Settings className="size-4" /> Settings
          </Link>
        </nav>
      </div>
    );
  }

  return (
    <div className="h-dvh-safe flex flex-col bg-base-100">
      {/* Top bar */}
      <header className="flex items-center gap-2 border-b border-base-300 px-2 py-1.5">
        <Link to="/" className="btn btn-ghost btn-sm btn-square" title="All environments">
          <ArrowLeft className="size-4" />
        </Link>
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate font-semibold">{env.name}</span>
          <PhaseBadge phase={env.phase} deleting={env.deleting} />
          <span className="hidden truncate text-xs text-base-content/60 md:inline">
            {env.image} · {env.resources.cpu ?? "?"} CPU · {env.resources.memory ? formatBytes(env.resources.memory) : "?"}
            {env.ip && ` · ${env.ip}`}
          </span>
          {env.owner !== me.data?.name && <span className="badge badge-soft badge-sm hidden lg:inline-flex">by {env.owner}</span>}
          {shared && (
            <span className="badge badge-soft badge-info badge-sm hidden gap-1 lg:inline-flex" title={[...env.sharing.users, ...env.sharing.groups.map((g) => `@${g}`)].join(", ")}>
              <Share2 className="size-3" /> shared
            </span>
          )}
          {agentRunning > 0 && (
            <button className="badge badge-secondary badge-sm gap-1" onClick={() => setLayout((l) => ({ ...l, side: "activity" }))}>
              <Spinner className="loading-xs" /> agent working
            </button>
          )}
        </div>
        <div className="flex-1" />
        <button className="btn btn-ghost btn-sm hidden text-base-content/60 lg:flex" onClick={() => window.dispatchEvent(new Event("devenv:palette"))}>
          <Search className="size-4" /> <kbd className="kbd kbd-xs">⌘K</kbd>
        </button>
        {layout.panes.length > 1 && (
          <button className="btn btn-ghost btn-sm btn-square" title="Toggle split direction" aria-label="Toggle split direction" onClick={() => setLayout((l) => ({ ...l, split: l.split === "horizontal" ? "vertical" : "horizontal" }))}>
            {layout.split === "horizontal" ? <Rows2 className="size-4" /> : <Columns2 className="size-4" />}
          </button>
        )}
        <button
          className="btn btn-ghost btn-sm btn-square"
          title="Split: open a terminal in a second pane"
          aria-label="Split"
          onClick={() => {
            if (layout.panes.length > 1) return;
            update((l) => ({ ...l, panes: [...l.panes, { tabs: [] }], focused: 1 }));
            openTerminal(undefined, 1);
          }}
          disabled={layout.panes.length > 1}
        >
          <Columns2 className="size-4" />
        </button>
        <PowerButton env={env} className="btn-sm" />
        <EnvMenu env={env} onDeleted={() => navigate("/")} />
        <span className="hidden lg:contents">
          <ThemeSwitch />
        </span>
        <UserMenu />
      </header>

      <div className="flex min-h-0 flex-1">
        {/* Main area */}
        <div className="min-w-0 flex-1">
          <Group orientation="horizontal" className="h-full">
            <Panel id="main" minSize="30">
              {layout.panes.length === 1 ? (
                paneView(0)
              ) : (
                <Group orientation={layout.split === "horizontal" ? "horizontal" : "vertical"} className="h-full">
                  <Panel id="p0" minSize="15">
                    {paneView(0)}
                  </Panel>
                  <Separator className={cx("resize-handle", layout.split === "horizontal" ? "w-1" : "h-1")} />
                  <Panel id="p1" minSize="15">
                    {paneView(1)}
                  </Panel>
                </Group>
              )}
            </Panel>
            {sideOpen && (
              <>
                <Separator className="resize-handle w-1" />
                <Panel id="side" defaultSize="26" minSize="16" maxSize="50">
                  <div className="flex h-full flex-col border-l border-base-300 bg-base-100">
                    {sideHeader}
                    <div className="min-h-0 flex-1 overflow-y-auto">{sideContent}</div>
                  </div>
                </Panel>
              </>
            )}
          </Group>
        </div>
        {/* Activity bar */}
        <nav className="flex w-11 flex-col items-center gap-1 border-l border-base-300 bg-base-200 py-2">
          {sidePanels.map((p) => (
            <button
              key={p.id}
              className={cx("btn btn-ghost btn-sm btn-square relative", layout.side === p.id && "btn-active text-primary")}
              title={p.label}
              onClick={() => setSide(p.id)}
            >
              {p.icon}
              {p.id === "activity" && agentRunning > 0 && <span className="badge badge-secondary badge-xs absolute -top-0.5 -right-0.5">{agentRunning}</span>}
            </button>
          ))}
          <div className="flex-1" />
          <Link to={`/env/${env.name}/settings`} className="btn btn-ghost btn-sm btn-square" title="Settings">
            <Settings className="size-4" />
          </Link>
        </nav>
      </div>
    </div>
  );
}

function NotReady({ env }: { env: Environment }) {
  const transitional = env.phase === "Starting" || env.phase === "Pending" || env.phase === "Stopping" || env.deleting;
  return (
    <div className="absolute inset-0 z-20 flex items-center justify-center bg-base-100/85 backdrop-blur-sm">
      <div className="max-w-md space-y-3 text-center">
        <PhaseBadge phase={env.phase} deleting={env.deleting} size="md" />
        <div className="text-sm text-base-content/70">
          {env.deleting
            ? "This environment is being deleted."
            : transitional
              ? env.message || "Preparing the environment…"
              : env.phase === "Failed"
                ? env.message || "The environment failed to start."
                : "The environment is stopped. Files in /home/dev are kept."}
        </div>
        {!env.running && !env.deleting && <PowerButton env={env} className="btn-md" />}
        {transitional && <span className="loading loading-bars loading-md text-primary" />}
      </div>
    </div>
  );
}
