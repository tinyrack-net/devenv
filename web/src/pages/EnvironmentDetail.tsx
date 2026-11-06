import { lazy, Suspense, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router";
import { api, ApiError, type Environment, type Resources } from "../api";
import {
  CopyButton,
  ErrorBox,
  PhaseBadge,
  isTransitional,
  timeAgo,
} from "../components/common";
import { ResourceInputs } from "../components/ResourceInputs";
import { formatBytes, formatCPU, gib } from "../quantity";

// Heavy viewers are loaded on demand.
const Terminal = lazy(() => import("../components/Terminal").then((m) => ({ default: m.Terminal })));
const VncViewer = lazy(() => import("../components/VncViewer").then((m) => ({ default: m.VncViewer })));

type Tab = "overview" | "terminal" | "desktop";

export function EnvironmentDetailPage() {
  const { name = "" } = useParams();
  const env = useQuery({
    queryKey: ["environments", name],
    queryFn: () => api.environment(name),
    refetchInterval: (q) =>
      q.state.data && isTransitional(q.state.data.phase, q.state.data.deleting)
        ? 2000
        : 10000,
  });

  if (env.error instanceof ApiError && env.error.status === 404) {
    return (
      <div className="card empty">
        <p>Environment “{name}” does not exist.</p>
        <Link to="/">Back to environments</Link>
      </div>
    );
  }
  if (!env.data) return <ErrorBox error={env.error} />;
  return <Detail env={env.data} />;
}

function Detail({ env }: { env: Environment }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [editing, setEditing] = useState(false);
  const [tab, setTab] = useState<Tab>("overview");
  const ready = env.phase === "Running" && !env.deleting;
  const invalidate = () => qc.invalidateQueries({ queryKey: ["environments"] });

  const toggle = useMutation({
    mutationFn: () =>
      env.running
        ? api.stopEnvironment(env.name)
        : api.startEnvironment(env.name),
    onSettled: invalidate,
  });
  const remove = useMutation({
    mutationFn: () => api.deleteEnvironment(env.name),
    onSuccess: async () => {
      await invalidate();
      navigate("/");
    },
  });

  const confirmDelete = () => {
    const typed = prompt(
      `This permanently deletes "${env.name}" including its home volume.\nType the name to confirm:`,
    );
    if (typed === env.name) remove.mutate();
  };

  return (
    <>
      <div className="page-header">
        <div>
          <div className="breadcrumb">
            <Link to="/">Environments</Link> /
          </div>
          <h1>
            {env.name} <PhaseBadge phase={env.phase} deleting={env.deleting} />
          </h1>
        </div>
        <div className="toolbar">
          {!env.deleting && (
            <>
              <button
                disabled={toggle.isPending}
                onClick={() => toggle.mutate()}
              >
                {env.running ? "Stop" : "Start"}
              </button>
              <button
                className="danger"
                disabled={remove.isPending}
                onClick={confirmDelete}
              >
                Delete
              </button>
            </>
          )}
        </div>
      </div>
      <ErrorBox error={toggle.error ?? remove.error} />
      {env.message && (
        <div className={env.phase === "Failed" ? "alert" : "notice"}>
          {env.message}
        </div>
      )}

      <div className="tabs">
        <button
          className={tab === "overview" ? "active" : ""}
          onClick={() => setTab("overview")}
        >
          Overview
        </button>
        <button
          className={tab === "terminal" ? "active" : ""}
          disabled={!ready}
          onClick={() => setTab("terminal")}
        >
          Terminal
        </button>
        {env.desktop && (
          <button
            className={tab === "desktop" ? "active" : ""}
            disabled={!ready}
            onClick={() => setTab("desktop")}
          >
            Desktop
          </button>
        )}
      </div>

      <Suspense fallback={<div className="card muted">Loading…</div>}>
        {tab === "terminal" && ready && (
          <Terminal env={env.name} onClose={() => setTab("overview")} />
        )}
        {tab === "desktop" && ready && (
          <VncViewer env={env.name} onClose={() => setTab("overview")} />
        )}
      </Suspense>
      {tab !== "overview" && !ready && (
        <div className="card muted">Start the environment to connect.</div>
      )}

      {tab === "overview" && (
        <div className="grid">
          <section className="card">
            <h2>Overview</h2>
            <dl className="props">
              <dt>Image</dt>
              <dd>
                {env.image}
                {env.desktop && <span className="tag">desktop</span>}
                {env.imageRef && (
                  <div className="small muted mono">{env.imageRef}</div>
                )}
              </dd>
              <dt>Class</dt>
              <dd>{env.class}</dd>
              <dt>Node</dt>
              <dd>{env.nodeName || "-"}</dd>
              <dt>IP</dt>
              <dd>
                {env.ip || (
                  <span className="muted">not assigned (gateway mode)</span>
                )}
              </dd>
              <dt>Owner</dt>
              <dd>{env.owner || "-"}</dd>
              <dt>Created</dt>
              <dd>{timeAgo(env.createdAt)}</dd>
            </dl>
          </section>

          <section className="card">
            <div className="card-header">
              <h2>Resources</h2>
              {!editing && !env.deleting && (
                <button className="small" onClick={() => setEditing(true)}>
                  Edit
                </button>
              )}
            </div>
            {editing ? (
              <EditResources env={env} onDone={() => setEditing(false)} />
            ) : (
              <dl className="props">
                <dt>CPU</dt>
                <dd>
                  {env.resources.cpu
                    ? formatCPU(env.resources.cpu)
                    : "image default"}
                </dd>
                <dt>Memory</dt>
                <dd>
                  {env.resources.memory
                    ? formatBytes(env.resources.memory)
                    : "image default"}
                </dd>
                <dt>Disk</dt>
                <dd>
                  {env.resources.disk
                    ? formatBytes(env.resources.disk)
                    : "class default"}
                </dd>
              </dl>
            )}
          </section>

          <section className="card">
            <h2>Connect</h2>
            <ConnectInfo env={env} />
          </section>

          <section className="card wide">
            <h2>Events</h2>
            <Events name={env.name} />
          </section>
        </div>
      )}
    </>
  );
}

function EditResources({
  env,
  onDone,
}: {
  env: Environment;
  onDone: () => void;
}) {
  const qc = useQueryClient();
  const [value, setValue] = useState<Resources>(env.resources);
  const save = useMutation({
    mutationFn: () => api.updateEnvironment(env.name, { resources: value }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["environments"] });
      onDone();
    },
  });
  return (
    <div>
      <ErrorBox error={save.error} />
      <ResourceInputs
        className={env.class}
        value={value}
        defaults={env.resources}
        onChange={setValue}
        exclude={env.name}
        minDisk={gib(env.resources.disk)}
        persistent
      />
      {env.running && (
        <p className="small muted">
          Saving restarts the environment. The home directory is kept.
        </p>
      )}
      <div className="form-actions">
        <button onClick={onDone}>Cancel</button>
        <button
          className="primary"
          disabled={save.isPending}
          onClick={() => save.mutate()}
        >
          Save
        </button>
      </div>
    </div>
  );
}

function ConnectInfo({ env }: { env: Environment }) {
  const info = useQuery({
    queryKey: ["info"],
    queryFn: api.info,
    staleTime: Infinity,
  });
  const ssh = info.data?.ssh;
  if (!ssh) return null;
  if (env.ip) {
    const cmd = `ssh dev@${env.ip}`;
    return (
      <div className="connect">
        <div className="small muted">SSH (dedicated IP)</div>
        <div className="codeline">
          <code>{cmd}</code>
          <CopyButton text={cmd} />
        </div>
      </div>
    );
  }
  if (!ssh.enabled)
    return (
      <div className="muted small">
        SSH gateway is disabled. Use the web terminal.
      </div>
    );

  const host = ssh.host || window.location.hostname;
  const portArg = ssh.port === 22 ? "" : ` -p ${ssh.port}`;
  const cmd = `ssh${portArg} ${env.name}@${host}`;
  const alias = `devenv-${env.name}`;
  const config = [
    `Host ${alias}`,
    `  HostName ${host}`,
    ...(ssh.port !== 22 ? [`  Port ${ssh.port}`] : []),
    `  User ${env.name}`,
  ].join("\n");
  const knownHost = ssh.hostKey
    ? `${ssh.port === 22 ? host : `[${host}]:${ssh.port}`} ${ssh.hostKey}`
    : "";

  return (
    <div className="connect">
      <div className="small muted">SSH</div>
      <div className="codeline">
        <code>{cmd}</code>
        <CopyButton text={cmd} />
      </div>
      <details>
        <summary className="small">
          ~/.ssh/config (VS Code / JetBrains remote)
        </summary>
        <pre className="block">{config}</pre>
        <CopyButton text={config} />
      </details>
      {ssh.fingerprint && (
        <details>
          <summary className="small">Gateway host key</summary>
          <div className="small mono">{ssh.fingerprint}</div>
          <pre className="block">{knownHost}</pre>
          <CopyButton text={knownHost} label="Copy known_hosts line" />
        </details>
      )}
      <div className="small muted">
        Authorized keys: {env.sshAuthorizedKeys.length}
        {env.sshAuthorizedKeys.length === 0 &&
          " — add a key to connect over SSH"}
      </div>
    </div>
  );
}

function Events({ name }: { name: string }) {
  const events = useQuery({
    queryKey: ["events", name],
    queryFn: () => api.events(name),
    refetchInterval: 5000,
  });
  if (events.error) return <ErrorBox error={events.error} />;
  if (!events.data?.length)
    return <div className="muted small">No events.</div>;
  return (
    <table className="table compact">
      <tbody>
        {events.data.map((e, i) => (
          <tr key={i}>
            <td className="small muted nowrap">{timeAgo(e.time)}</td>
            <td>
              <span
                className={`badge ${e.type === "Warning" ? "bad" : "idle"}`}
              >
                {e.reason}
              </span>
            </td>
            <td className="small">{e.message}</td>
            <td className="small muted nowrap">{e.object}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
