import { useEffect, useState, type ReactNode } from "react";
import { Link, NavLink, Outlet, useLocation, useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import {
  Boxes,
  ChevronDown,
  Cpu,
  KeyRound,
  LayoutGrid,
  LogOut,
  Menu,
  Monitor,
  Moon,
  Plus,
  Server,
  Settings,
  Shield,
  Sun,
  Ticket,
  User as UserIcon,
  Users,
  Activity,
  Layers,
} from "lucide-react";
import { api } from "../lib/api";
import { useEventStream, useStreamStatus } from "../lib/stream";
import { useInfo, useMe, usePrefs, useSyncPrefs, type Theme } from "../lib/prefs";
import { CommandPalette, useCommandPalette } from "./CommandPalette";
import { Loading, cx } from "./ui";

function Item({ to, icon, children, end }: { to: string; icon: ReactNode; children: ReactNode; end?: boolean }) {
  return (
    <li>
      <NavLink to={to} end={end} className={({ isActive }) => cx(isActive && "menu-active")}>
        {icon}
        {children}
      </NavLink>
    </li>
  );
}

export function ThemeSwitch() {
  const [prefs, update] = usePrefs();
  const next: Record<Theme, Theme> = { system: "light", light: "dark", dark: "system" };
  const icon = prefs.theme === "light" ? <Sun className="size-4" /> : prefs.theme === "dark" ? <Moon className="size-4" /> : <Monitor className="size-4" />;
  return (
    <button className="btn btn-ghost btn-sm btn-square" title={`Theme: ${prefs.theme}`} onClick={() => update({ theme: next[prefs.theme] })}>
      {icon}
    </button>
  );
}

export function UserMenu() {
  const me = useMe();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const info = useInfo();
  if (!me.data) return null;
  const name = me.data.displayName || me.data.name;
  return (
    <div className="dropdown dropdown-end">
      <div tabIndex={0} role="button" className="btn btn-ghost btn-sm gap-2">
        <div className="avatar avatar-placeholder">
          <div className="w-6 rounded-full bg-primary text-primary-content">
            <span className="text-xs">{name.slice(0, 1).toUpperCase()}</span>
          </div>
        </div>
        <span className="hidden max-w-32 truncate sm:inline">{name}</span>
        <ChevronDown className="size-3.5" />
      </div>
      <ul tabIndex={0} className="dropdown-content menu z-30 mt-2 w-60 rounded-box bg-base-100 p-2 shadow-lg">
        <li className="menu-title">
          <span className="truncate">{me.data.email || me.data.name}</span>
          <span className="text-xs font-normal">{me.data.admin ? "Administrator" : "User"}</span>
        </li>
        <li>
          <Link to="/settings/profile">
            <UserIcon className="size-4" /> Profile
          </Link>
        </li>
        <li>
          <Link to="/settings/keys">
            <KeyRound className="size-4" /> SSH keys
          </Link>
        </li>
        <li>
          <Link to="/settings/tokens">
            <Ticket className="size-4" /> Access tokens
          </Link>
        </li>
        <li>
          <Link to="/settings/preferences">
            <Settings className="size-4" /> Preferences
          </Link>
        </li>
        {!info.data?.auth.disabled && (
          <li>
            <button
              onClick={async () => {
                await api.logout();
                qc.clear();
                navigate("/login");
              }}
            >
              <LogOut className="size-4" /> Sign out
            </button>
          </li>
        )}
      </ul>
    </div>
  );
}

function StreamIndicator() {
  const s = useStreamStatus();
  if (s === "open") return null;
  return (
    <span className="badge badge-warning badge-sm gap-1" title="Live updates reconnecting">
      <span className="loading loading-dots loading-xs" /> reconnecting
    </span>
  );
}

/** Shell for every signed-in page: event stream, preferences, palette. */
export function AuthShell() {
  const me = useMe();
  useSyncPrefs(me.data);
  useEventStream();
  const palette = useCommandPalette();
  if (me.isLoading) return <Loading />;
  return (
    <>
      <Outlet />
      <CommandPalette {...palette} />
    </>
  );
}

/** Sidebar layout for management pages. */
export function Layout() {
  const me = useMe();
  const info = useInfo();
  const loc = useLocation();
  const [open, setOpen] = useState(false);
  useEffect(() => setOpen(false), [loc.pathname]);
  const admin = me.data?.admin;
  return (
    <div className="drawer lg:drawer-open">
      <input id="nav" type="checkbox" className="drawer-toggle" checked={open} onChange={(e) => setOpen(e.target.checked)} />
      <div className="drawer-content flex min-h-screen flex-col">
        <header className="navbar sticky top-0 z-20 gap-2 border-b border-base-300 bg-base-100/90 px-4 backdrop-blur">
          <label htmlFor="nav" className="btn btn-ghost btn-sm btn-square lg:hidden">
            <Menu className="size-5" />
          </label>
          <div className="flex-1" />
          <StreamIndicator />
          <button
            className="btn btn-ghost btn-sm hidden gap-2 text-base-content/60 sm:flex"
            onClick={() => window.dispatchEvent(new Event("devenv:palette"))}
          >
            Search… <kbd className="kbd kbd-xs">⌘K</kbd>
          </button>
          <ThemeSwitch />
          <UserMenu />
        </header>
        <main className="mx-auto w-full max-w-7xl flex-1 p-4 sm:p-6">
          <Outlet />
        </main>
      </div>
      <div className="drawer-side z-30">
        <label htmlFor="nav" className="drawer-overlay" />
        <aside className="flex min-h-full w-64 flex-col border-r border-base-300 bg-base-100">
          <Link to="/" className="flex items-center gap-2 px-5 py-4 text-lg font-semibold">
            <Boxes className="size-6 text-primary" /> devenv
          </Link>
          <div className="px-3">
            <Link to="/new" className="btn btn-primary btn-sm w-full">
              <Plus className="size-4" /> New environment
            </Link>
          </div>
          <ul className="menu w-full flex-1 gap-0.5 px-3 py-4">
            <Item to="/" end icon={<LayoutGrid className="size-4" />}>
              Environments
            </Item>
            <Item to="/images" icon={<Layers className="size-4" />}>
              Images
            </Item>
            <li className="menu-title mt-3">Account</li>
            <Item to="/settings/keys" icon={<KeyRound className="size-4" />}>
              SSH keys
            </Item>
            <Item to="/settings/tokens" icon={<Ticket className="size-4" />}>
              Access tokens
            </Item>
            <Item to="/settings/preferences" icon={<Settings className="size-4" />}>
              Preferences
            </Item>
            {admin && (
              <>
                <li className="menu-title mt-3 flex-row items-center gap-1">
                  <Shield className="size-3" /> Administration
                </li>
                <Item to="/admin/users" icon={<Users className="size-4" />}>
                  Users
                </Item>
                <Item to="/admin/environments" icon={<Server className="size-4" />}>
                  All environments
                </Item>
                <Item to="/admin/classes" icon={<Cpu className="size-4" />}>
                  Classes
                </Item>
                <Item to="/admin/tokens" icon={<Ticket className="size-4" />}>
                  Tokens
                </Item>
                <Item to="/admin/activity" icon={<Activity className="size-4" />}>
                  Agent activity
                </Item>
                <Item to="/admin/system" icon={<Settings className="size-4" />}>
                  System
                </Item>
              </>
            )}
          </ul>
          <div className="px-5 py-3 text-xs text-base-content/50">devenv {info.data?.version}</div>
        </aside>
      </div>
    </div>
  );
}

/** Page header used by management pages. */
export function PageHeader({ title, subtitle, actions }: { title: ReactNode; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-3">
      <div>
        <h1 className="text-2xl font-semibold">{title}</h1>
        {subtitle && <p className="mt-1 text-sm text-base-content/60">{subtitle}</p>}
      </div>
      {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
    </div>
  );
}
