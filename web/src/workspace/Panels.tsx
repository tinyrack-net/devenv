import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, Play, ScrollText, Square, Trash2 } from "lucide-react";
import { api, type Environment, type Process } from "../lib/api";
import { useInfo } from "../lib/prefs";
import { editorLinks, knownHostsLine, portForward, sshCommand, sshConfig, sshTargets } from "../lib/connect";
import { formatBytes } from "../lib/quantity";
import { CopyButton, CopyField, ErrorAlert, cx, timeAgo } from "../components/ui";
import { useToast } from "../components/toast";

function PanelMessage({ children }: { children: React.ReactNode }) {
  return <div className="p-4 text-sm text-base-content/60">{children}</div>;
}

export function ProcessesPanel({ env, ready, onOpenLog }: { env: string; ready: boolean; onOpenLog: (p: Process) => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const list = useQuery({ queryKey: ["processes", env], queryFn: () => api.processes(env), enabled: ready, refetchInterval: 5000 });
  const [cmd, setCmd] = useState("");
  const [id, setId] = useState("");
  const start = useMutation({
    mutationFn: () => api.startProcess(env, { id: id || `p${Date.now() % 100000}`, command: cmd }),
    onSuccess: (out) => {
      setCmd("");
      setId("");
      qc.invalidateQueries({ queryKey: ["processes", env] });
      onOpenLog(out.process);
    },
    onError: (e) => toast((e as Error).message, "error"),
  });
  const stop = useMutation({
    mutationFn: ({ pid, remove }: { pid: string; remove: boolean }) => api.stopProcess(env, pid, remove),
    onSettled: () => qc.invalidateQueries({ queryKey: ["processes", env] }),
  });
  if (!ready) return <PanelMessage>Start the environment to see its processes.</PanelMessage>;
  return (
    <div>
      <ErrorAlert error={list.error} className="m-2" />
      <ul>
        {list.data?.length === 0 && <PanelMessage>No background processes. Agents start them with start_process; you can too.</PanelMessage>}
        {list.data?.map((p) => (
          <li key={p.id} className="border-b border-base-300 px-3 py-2">
            <div className="flex items-center gap-2">
              <span className={cx("status", p.running ? "status-success" : p.exit_code ? "status-error" : "")} />
              <span className="font-medium">{p.id}</span>
              <span className="text-xs text-base-content/50">{p.running ? `pid ${p.pid}` : `exit ${p.exit_code ?? p.signal ?? "?"}`}</span>
              <div className="ml-auto flex gap-0.5">
                <button className="btn btn-ghost btn-xs" title="Output" onClick={() => onOpenLog(p)}>
                  <ScrollText className="size-3.5" />
                </button>
                {p.running ? (
                  <button className="btn btn-ghost btn-xs" title="Stop" onClick={() => stop.mutate({ pid: p.id, remove: false })}>
                    <Square className="size-3.5" />
                  </button>
                ) : (
                  <button className="btn btn-ghost btn-xs" title="Remove" onClick={() => stop.mutate({ pid: p.id, remove: true })}>
                    <Trash2 className="size-3.5" />
                  </button>
                )}
              </div>
            </div>
            <div className="font-mono-tight mt-1 truncate text-base-content/70" title={p.command}>
              {p.command}
            </div>
            <div className="text-[11px] text-base-content/50">
              started {timeAgo(p.started_at)} · {formatBytes(p.output_bytes)} output
            </div>
          </li>
        ))}
      </ul>
      <form
        className="space-y-1 p-3"
        onSubmit={(e) => {
          e.preventDefault();
          start.mutate();
        }}
      >
        <div className="text-xs font-medium text-base-content/60">Start a background process</div>
        <input className="input input-sm font-mono-tight w-full" placeholder="pnpm dev" value={cmd} onChange={(e) => setCmd(e.target.value)} />
        <div className="flex gap-1">
          <input className="input input-sm flex-1" placeholder="id (optional)" value={id} onChange={(e) => setId(e.target.value.toLowerCase())} />
          <button className="btn btn-sm btn-primary" disabled={!cmd || start.isPending}>
            <Play className="size-3.5" /> Start
          </button>
        </div>
      </form>
    </div>
  );
}

/** Live output of a background process, as a workspace tab. */
export function ProcessLog({ env, id, active }: { env: string; id: string; active: boolean }) {
  const [text, setText] = useState("");
  const [proc, setProc] = useState<Process | null>(null);
  const [follow, setFollow] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const pre = useRef<HTMLPreElement>(null);
  useEffect(() => {
    let stop = false;
    let offset = -1;
    (async () => {
      while (!stop) {
        try {
          const out = await api.processOutput(env, id, offset, offset < 0 ? 0 : 20);
          if (stop) return;
          setError(null);
          setProc(out.process);
          if (offset < 0 || out.dropped) setText(out.output);
          else if (out.output) setText((t) => (t + out.output).slice(-2_000_000));
          offset = out.next_offset;
          if (!out.process.running && !out.output) await new Promise((r) => setTimeout(r, 5000));
        } catch (e) {
          setError(e);
          await new Promise((r) => setTimeout(r, 3000));
        }
      }
    })();
    return () => {
      stop = true;
    };
  }, [env, id]);
  useEffect(() => {
    if (follow && pre.current) pre.current.scrollTop = pre.current.scrollHeight;
  }, [text, follow, active]);
  return (
    <div className="flex h-full flex-col bg-base-100">
      <div className="flex items-center gap-2 border-b border-base-300 px-3 py-1 text-xs">
        <span className={cx("status", proc?.running ? "status-success" : "status-error")} />
        <span className="font-mono-tight truncate">{proc?.command}</span>
        <span className="text-base-content/50">{proc && (proc.running ? `running · pid ${proc.pid}` : `exited ${proc.exit_code ?? proc.signal}`)}</span>
        <div className="flex-1" />
        <label className="label gap-1 text-xs">
          <input type="checkbox" className="checkbox checkbox-xs" checked={follow} onChange={(e) => setFollow(e.target.checked)} /> follow
        </label>
        <CopyButton text={text} />
      </div>
      <ErrorAlert error={error} className="m-2" />
      <pre ref={pre} className="font-mono-tight flex-1 overflow-auto p-3 whitespace-pre-wrap" onScroll={(e) => {
        const el = e.currentTarget;
        setFollow(el.scrollHeight - el.scrollTop - el.clientHeight < 40);
      }}>
        {text}
      </pre>
    </div>
  );
}

export function PortsPanel({ env, ready }: { env: Environment; ready: boolean }) {
  const info = useInfo();
  const ports = useQuery({ queryKey: ["ports", env.name], queryFn: () => api.ports(env.name), enabled: ready, refetchInterval: 5000 });
  const target = sshTargets(env, info.data)[0];
  if (!ready) return <PanelMessage>Start the environment to see listening ports.</PanelMessage>;
  return (
    <div>
      <ErrorAlert error={ports.error} className="m-2" />
      {ports.data?.length === 0 && <PanelMessage>Nothing is listening yet. Start a server (for example on port 3000) and it shows up here.</PanelMessage>}
      <ul>
        {ports.data?.map((p) => (
          <li key={`${p.port}-${p.address}`} className="space-y-1 border-b border-base-300 px-3 py-2">
            <div className="flex items-center gap-2">
              <span className="font-mono font-semibold">{p.port}</span>
              <span className="text-xs text-base-content/50">
                {p.process ?? ""} {p.local ? "· localhost only" : ""}
              </span>
              <div className="ml-auto flex gap-1">
                {p.url && (
                  <a className="btn btn-ghost btn-xs" href={p.url} target="_blank" rel="noreferrer" title={p.url}>
                    <ExternalLink className="size-3.5" /> IP
                  </a>
                )}
              </div>
            </div>
            {target && (
              <div className="flex items-center gap-1">
                <code className="font-mono-tight flex-1 truncate text-base-content/60">{portForward(target, p.port)}</code>
                <CopyButton text={portForward(target, p.port)} />
              </div>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

export function StatusPanel({ env, ready }: { env: Environment; ready: boolean }) {
  const stats = useQuery({ queryKey: ["stats", env.name], queryFn: () => api.stats(env.name), enabled: ready, refetchInterval: 10_000 });
  const events = useQuery({ queryKey: ["events", env.name], queryFn: () => api.events(env.name), refetchInterval: 10_000 });
  const s = stats.data;
  return (
    <div className="space-y-4 p-3 text-sm">
      {s && (
        <div className="space-y-3">
          {s.memory_limit ? (
            <div>
              <div className="flex justify-between text-xs">
                <span>Memory</span>
                <span>
                  {formatBytes(s.memory_used ?? 0)} / {formatBytes(s.memory_limit)}
                </span>
              </div>
              <progress className="progress progress-primary h-1.5" value={s.memory_used ?? 0} max={s.memory_limit} />
            </div>
          ) : null}
          <div>
            <div className="flex justify-between text-xs">
              <span>/home/dev</span>
              <span>
                {formatBytes(s.disk_used)} / {formatBytes(s.disk_total)}
              </span>
            </div>
            <progress className={cx("progress h-1.5", s.disk_used / s.disk_total > 0.9 ? "progress-error" : "progress-primary")} value={s.disk_used} max={s.disk_total} />
          </div>
          <div className="text-xs text-base-content/60">
            {s.processes} processes{s.oom_kills > 0 && <span className="text-error"> · {s.oom_kills} out-of-memory kills</span>}
          </div>
        </div>
      )}
      <div>
        <div className="mb-1 text-xs font-medium text-base-content/60">Events</div>
        <ul className="space-y-1">
          {events.data?.length === 0 && <li className="text-xs text-base-content/50">No recent events.</li>}
          {events.data?.slice(0, 30).map((e, i) => (
            <li key={i} className={cx("text-xs", e.type === "Warning" && "text-warning")}>
              <span className="text-base-content/50">{timeAgo(e.time)}</span> <span className="font-medium">{e.reason}</span> {e.message}
              {e.count > 1 && <span className="text-base-content/50"> ×{e.count}</span>}
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}

/** SSH, editor and address information. */
export function ConnectPanel({ env }: { env: Environment }) {
  const info = useInfo();
  const targets = sshTargets(env, info.data);
  const t = targets[0];
  return (
    <div className="space-y-4 p-3 text-sm">
      {targets.length === 0 && <div className="text-base-content/60">SSH is not available for this environment.</div>}
      {targets.map((x) => (
        <div key={x.host + x.port} className="space-y-1">
          <div className="text-xs font-medium text-base-content/60">{x.direct ? "Direct (own address)" : "Through the gateway"}</div>
          <CopyField value={sshCommand(x)} />
        </div>
      ))}
      {t && (
        <>
          <div className="space-y-1">
            <div className="text-xs font-medium text-base-content/60">~/.ssh/config</div>
            <CopyField value={sshConfig(env, t)} />
          </div>
          <div className="space-y-1">
            <div className="text-xs font-medium text-base-content/60">Open in an editor (Remote-SSH, after adding the config above)</div>
            <div className="flex flex-wrap gap-1">
              {editorLinks(env).map((l) => (
                <a key={l.name} className="btn btn-sm" href={l.url}>
                  {l.name}
                </a>
              ))}
            </div>
            <div className="text-xs text-base-content/50">JetBrains Gateway: new SSH connection to the same host, user and port.</div>
          </div>
          {knownHostsLine(info.data, t) && (
            <div className="space-y-1">
              <div className="text-xs font-medium text-base-content/60">Gateway host key (known_hosts)</div>
              <CopyField value={knownHostsLine(info.data, t)!} />
            </div>
          )}
        </>
      )}
      <div className="text-xs text-base-content/60">
        Your SSH keys{env.sharing.users.length || env.sharing.groups.length ? " and those of people it is shared with" : ""} are accepted. {env.passwordSet ? "Password login is enabled." : "Password login is off."}
      </div>
    </div>
  );
}
