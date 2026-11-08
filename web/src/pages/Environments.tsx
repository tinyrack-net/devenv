import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { Bot, LayoutGrid, List, Monitor, Plus, Search, Server, Share2, SquareTerminal } from "lucide-react";
import { api, type Environment } from "../lib/api";
import { useInfo, useMe, usePrefs } from "../lib/prefs";
import { useIsMobile } from "../lib/media";
import { formatBytes, formatCPU, ratio } from "../lib/quantity";
import { sshCommand, sshTargets } from "../lib/connect";
import { PageHeader } from "../components/Layout";
import { CopyButton, Empty, ErrorAlert, Loading, Meter, PhaseBadge, cx, timeAgo } from "../components/ui";
import { EnvMenu, PowerButton } from "../components/EnvActions";

type Filter = "all" | "mine" | "shared" | "agent" | "running";

export function UsageSummary() {
  const usage = useQuery({ queryKey: ["usage"], queryFn: api.myUsage, refetchInterval: 30_000 });
  const u = usage.data;
  if (!u) return null;
  const l = u.limits;
  const limited = l.maxEnvironments || l.maxRunning || l.cpu || l.memory || l.disk;
  if (!limited) return null;
  return (
    <div className="mb-4 grid grid-cols-2 gap-3 rounded-box bg-base-100 p-4 sm:mb-6 sm:grid-cols-5 sm:gap-4">
      <Meter
        value={l.maxEnvironments ? u.environments / l.maxEnvironments : undefined}
        label={`Environments ${u.environments}${l.maxEnvironments ? ` / ${l.maxEnvironments}` : ""}`}
      />
      <Meter value={l.maxRunning ? u.running / l.maxRunning : undefined} label={`Running ${u.running}${l.maxRunning ? ` / ${l.maxRunning}` : ""}`} />
      <Meter value={ratio(u.cpu, l.cpu)} label={`CPU ${formatCPU(u.cpu)}${l.cpu ? ` / ${formatCPU(l.cpu)}` : ""}`} />
      <Meter value={ratio(u.memory, l.memory)} label={`Memory ${formatBytes(u.memory)}${l.memory ? ` / ${formatBytes(l.memory)}` : ""}`} />
      <Meter value={ratio(u.disk, l.disk)} label={`Disk ${formatBytes(u.disk)}${l.disk ? ` / ${formatBytes(l.disk)}` : ""}`} />
    </div>
  );
}

function OwnerBadges({ env, me }: { env: Environment; me?: string }) {
  return (
    <span className="flex flex-wrap gap-1">
      {env.createdBy === "mcp" && (
        <span className="badge badge-soft badge-secondary badge-sm gap-1" title="Created by an agent">
          <Bot className="size-3" /> agent
        </span>
      )}
      {env.owner !== me && <span className="badge badge-soft badge-sm">by {env.owner}</span>}
      {(env.sharing.users.length > 0 || env.sharing.groups.length > 0) && (
        <span className="badge badge-soft badge-info badge-sm gap-1" title={[...env.sharing.users, ...env.sharing.groups.map((g) => `@${g}`)].join(", ")}>
          <Share2 className="size-3" /> shared
        </span>
      )}
    </span>
  );
}

function EnvCard({ env, me }: { env: Environment; me?: string }) {
  const info = useInfo();
  const navigate = useNavigate();
  const ssh = sshTargets(env, info.data)[0];
  const ready = env.phase === "Running" && !env.deleting;
  return (
    <div className="card min-w-0 border border-base-300 bg-base-100 transition-shadow hover:shadow-md">
      <div className="card-body gap-3 p-4 sm:p-5">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <Link to={`/env/${env.name}`} className="link-hover block truncate text-lg font-semibold">
              {env.name}
            </Link>
            <div className="truncate text-sm text-base-content/60">
              {env.image} · {env.class}
            </div>
          </div>
          <div className="flex items-center gap-1">
            <PhaseBadge phase={env.phase} deleting={env.deleting} />
            <EnvMenu env={env} />
          </div>
        </div>
        <OwnerBadges env={env} me={me} />
        <div className="grid grid-cols-3 gap-2 text-xs text-base-content/70">
          <div>
            <div className="text-base-content/50">CPU</div>
            {env.resources.cpu ?? "default"}
          </div>
          <div>
            <div className="text-base-content/50">Memory</div>
            {env.resources.memory ? formatBytes(env.resources.memory) : "default"}
          </div>
          <div>
            <div className="text-base-content/50">{env.ip ? "IP" : "Created"}</div>
            {env.ip ?? timeAgo(env.createdAt)}
          </div>
        </div>
        {env.message && env.phase !== "Running" && <div className="text-xs text-base-content/60 line-clamp-2">{env.message}</div>}
        {ssh && (
          <div className="flex items-center gap-1 rounded-field bg-base-200 px-2 py-1">
            <code className="font-mono-tight flex-1 truncate">{sshCommand(ssh)}</code>
            <CopyButton text={sshCommand(ssh)} />
          </div>
        )}
        <div className="card-actions flex-nowrap items-center justify-between pt-1">
          <PowerButton env={env} />
          <div className="flex gap-1">
            <button className="btn btn-sm btn-ghost" disabled={!ready} onClick={() => navigate(`/env/${env.name}?open=terminal`)} title="Terminal">
              <SquareTerminal className="size-4" />
            </button>
            {env.desktop && (
              <button className="btn btn-sm btn-ghost" disabled={!ready} onClick={() => navigate(`/env/${env.name}?open=desktop`)} title="Desktop">
                <Monitor className="size-4" />
              </button>
            )}
            <Link to={`/env/${env.name}`} className="btn btn-sm btn-primary btn-soft">
              Open
            </Link>
          </div>
        </div>
      </div>
    </div>
  );
}

export function EnvTable({ envs, me, showOwner }: { envs: Environment[]; me?: string; showOwner?: boolean }) {
  const mobile = useIsMobile();
  if (mobile)
    return (
      <div className="grid gap-3">
        {envs.map((e) => (
          <EnvCard key={e.name} env={e} me={showOwner ? undefined : me} />
        ))}
      </div>
    );
  return (
    <div className="overflow-x-auto rounded-box border border-base-300 bg-base-100">
      <table className="table">
        <thead>
          <tr>
            <th>Name</th>
            {showOwner && <th>Owner</th>}
            <th>Status</th>
            <th>Image</th>
            <th>Resources</th>
            <th>Address</th>
            <th>Created</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {envs.map((env) => (
            <tr key={env.name} className="hover:bg-base-200/50">
              <td>
                <Link to={`/env/${env.name}`} className="link-hover font-medium">
                  {env.name}
                </Link>
                <div className="mt-1">
                  <OwnerBadges env={env} me={showOwner ? env.owner : me} />
                </div>
              </td>
              {showOwner && <td>{env.owner}</td>}
              <td>
                <PhaseBadge phase={env.phase} deleting={env.deleting} />
              </td>
              <td className="text-sm">
                {env.image}
                <div className="text-xs text-base-content/50">{env.class}</div>
              </td>
              <td className="text-sm">
                {env.resources.cpu ?? "-"} CPU · {env.resources.memory ? formatBytes(env.resources.memory) : "-"}
              </td>
              <td className="font-mono-tight">{env.ip ?? "-"}</td>
              <td className="text-sm">{timeAgo(env.createdAt)}</td>
              <td>
                <div className="flex justify-end gap-1">
                  <PowerButton env={env} className="btn-xs" />
                  <EnvMenu env={env} />
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function EnvironmentsPage() {
  const me = useMe();
  const mobile = useIsMobile();
  const [prefs, updatePrefs] = usePrefs();
  const [filter, setFilter] = useState<Filter>("all");
  const [q, setQ] = useState("");
  const envs = useQuery({ queryKey: ["environments", {}], queryFn: () => api.environments() });

  const list = useMemo(() => {
    const name = me.data?.name;
    return (envs.data ?? []).filter((e) => {
      if (q && !`${e.name} ${e.image} ${e.owner}`.toLowerCase().includes(q.toLowerCase())) return false;
      switch (filter) {
        case "mine":
          return e.owner === name;
        case "shared":
          return e.owner !== name;
        case "agent":
          return e.createdBy === "mcp";
        case "running":
          return e.running;
      }
      return true;
    });
  }, [envs.data, filter, q, me.data]);

  const counts = useMemo(() => {
    const d = envs.data ?? [];
    return { running: d.filter((e) => e.phase === "Running").length, total: d.length };
  }, [envs.data]);

  return (
    <>
      <PageHeader
        title="Environments"
        subtitle={envs.data ? `${counts.running} running of ${counts.total}` : undefined}
        actions={
          <Link to="/new" className="btn btn-primary btn-sm">
            <Plus className="size-4" /> New environment
          </Link>
        }
      />
      <UsageSummary />
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <label className="input input-sm min-w-0 flex-1 sm:w-64 sm:flex-none">
          <Search className="size-4 opacity-50" />
          <input placeholder="Filter" value={q} onChange={(e) => setQ(e.target.value)} />
        </label>
        <div className="order-last -mx-3 w-[calc(100%+1.5rem)] overflow-x-auto px-3 sm:order-none sm:mx-0 sm:w-auto sm:px-0">
          <div className="join">
            {(["all", "mine", "shared", "agent", "running"] as Filter[]).map((f) => (
              <button key={f} className={cx("btn btn-sm join-item whitespace-nowrap capitalize", filter === f && "btn-active")} onClick={() => setFilter(f)}>
                {f === "shared" ? "Shared with me" : f === "agent" ? "By agents" : f}
              </button>
            ))}
          </div>
        </div>
        <div className="hidden flex-1 sm:block" />
        <div className="join hidden sm:inline-flex">
          <button className={cx("btn btn-sm join-item", prefs.listView === "cards" && "btn-active")} onClick={() => updatePrefs({ listView: "cards" })} title="Cards">
            <LayoutGrid className="size-4" />
          </button>
          <button className={cx("btn btn-sm join-item", prefs.listView === "table" && "btn-active")} onClick={() => updatePrefs({ listView: "table" })} title="Table">
            <List className="size-4" />
          </button>
        </div>
      </div>
      <ErrorAlert error={envs.error} />
      {envs.isLoading && <Loading />}
      {envs.data && list.length === 0 && (
        <Empty icon={<Server className="size-10" />} title={envs.data.length === 0 ? "No environments yet" : "Nothing matches"}>
          {envs.data.length === 0 && (
            <Link to="/new" className="btn btn-primary btn-sm mt-2">
              Create your first environment
            </Link>
          )}
        </Empty>
      )}
      {list.length > 0 &&
        (prefs.listView === "table" && !mobile ? (
          <EnvTable envs={list} me={me.data?.name} />
        ) : (
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {list.map((e) => (
              <EnvCard key={e.name} env={e} me={me.data?.name} />
            ))}
          </div>
        ))}
    </>
  );
}
