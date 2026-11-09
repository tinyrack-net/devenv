import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Ticket, Trash2 } from "lucide-react";
import { api, type CreatedToken, type Token } from "../../lib/api";
import { useInfo } from "../../lib/prefs";
import { PageHeader } from "../../components/Layout";
import { Confirm, CopyField, Empty, ErrorAlert, Field, Loading, Modal, Spinner, formatTime, timeAgo, timeUntil } from "../../components/ui";

export function TokenTable({ tokens, onRevoke, showUser }: { tokens: Token[]; onRevoke: (t: Token) => void; showUser?: boolean }) {
  return (
    <div className="overflow-x-auto rounded-box border border-base-300 bg-base-100">
      <table className="table table-stack">
        <thead>
          <tr>
            <th>Name</th>
            {showUser && <th>User</th>}
            <th>Scopes</th>
            <th>Created</th>
            <th>Expires</th>
            <th>Last used</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {tokens.map((t) => {
            const expired = t.expiresAt && new Date(t.expiresAt) < new Date();
            return (
              <tr key={t.id}>
                <td>
                  <div className="font-medium">{t.name}</div>
                  <code className="font-mono-tight text-base-content/50">dvn_{t.id}_…</code>
                </td>
                {showUser && <td data-label="User">{t.user}</td>}
                <td data-label="Scopes">
                  <div className="flex gap-1">
                    {t.scopes.map((s) => (
                      <span key={s} className="badge badge-sm">
                        {s}
                      </span>
                    ))}
                  </div>
                </td>
                <td data-label="Created" className="text-sm">{timeAgo(t.createdAt)}</td>
                <td data-label="Expires" className="text-sm">
                  {t.expiresAt ? (
                    <span className={expired ? "text-error" : ""} title={formatTime(t.expiresAt)}>
                      {expired ? "expired " : "in "}
                      {timeUntil(t.expiresAt)}
                    </span>
                  ) : (
                    <span className="text-base-content/60">never</span>
                  )}
                </td>
                <td data-label="Last used" className="text-sm">{t.lastUsedAt ? timeAgo(t.lastUsedAt) : <span className="text-base-content/60">never</span>}</td>
                <td className="text-right max-md:-mt-8 max-md:text-right">
                  <button className="btn btn-ghost btn-sm text-error" onClick={() => onRevoke(t)}>
                    <Trash2 className="size-4" />
                  </button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function McpSetup({ token }: { token: string }) {
  const info = useInfo();
  const url = `${window.location.origin}/mcp`;
  const json = JSON.stringify(
    { mcpServers: { devenv: { type: "streamable-http", url, headers: { Authorization: `Bearer ${token}` } } } },
    null,
    2,
  );
  if (!info.data?.mcp) return null;
  return (
    <div className="space-y-2 text-sm">
      <div className="font-medium">Connect an MCP client</div>
      <p className="text-base-content/60">Streamable HTTP endpoint with a bearer token. Tools act as you: agents see and change only your environments.</p>
      <CopyField value={url} />
      <CopyField value={json} />
    </div>
  );
}

export function TokensPage() {
  const qc = useQueryClient();
  const tokens = useQuery({ queryKey: ["tokens"], queryFn: api.tokens });
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>(["mcp"]);
  const [days, setDays] = useState(90);
  const [created, setCreated] = useState<CreatedToken | null>(null);
  const [revoke, setRevoke] = useState<Token | null>(null);
  const create = useMutation({
    mutationFn: () => api.createToken({ name, scopes, expiresInDays: days }),
    onSuccess: (t) => {
      setCreated(t);
      setOpen(false);
      setName("");
      qc.invalidateQueries({ queryKey: ["tokens"] });
    },
  });
  const del = useMutation({
    mutationFn: (id: string) => api.deleteToken(id),
    onSuccess: () => {
      setRevoke(null);
      qc.invalidateQueries({ queryKey: ["tokens"] });
    },
  });
  const toggle = (s: string) => setScopes((x) => (x.includes(s) ? x.filter((y) => y !== s) : [...x, s]));
  return (
    <>
      <PageHeader
        title="Access tokens"
        subtitle="Personal tokens for AI agents (MCP) and scripts (REST API). They act with your permissions."
        actions={
          <button className="btn btn-primary btn-sm" onClick={() => setOpen(true)}>
            <Ticket className="size-4" /> New token
          </button>
        }
      />
      <ErrorAlert error={tokens.error} />
      {tokens.isLoading && <Loading />}
      {tokens.data?.length === 0 && (
        <Empty icon={<Ticket className="size-10" />} title="No tokens">
          Create a token to connect an agent such as Open WebUI, Claude or Codex through MCP.
        </Empty>
      )}
      {!!tokens.data?.length && <TokenTable tokens={tokens.data} onRevoke={setRevoke} />}

      <Modal
        open={open}
        onClose={() => setOpen(false)}
        title="New access token"
        actions={
          <>
            <button className="btn btn-ghost" onClick={() => setOpen(false)}>
              Cancel
            </button>
            <button className="btn btn-primary" disabled={!name || scopes.length === 0 || create.isPending} onClick={() => create.mutate()}>
              {create.isPending && <Spinner className="loading-xs" />} Create
            </button>
          </>
        }
      >
        <Field label="Name" hint="Where the token is used, e.g. “Open WebUI”.">
          <input className="input w-full" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
        </Field>
        <Field label="Scopes">
          <label className="label cursor-pointer gap-2 text-sm text-base-content">
            <input type="checkbox" className="checkbox checkbox-sm" checked={scopes.includes("mcp")} onChange={() => toggle("mcp")} /> mcp: AI agents through /mcp
          </label>
          <label className="label cursor-pointer gap-2 text-sm text-base-content">
            <input type="checkbox" className="checkbox checkbox-sm" checked={scopes.includes("api")} onChange={() => toggle("api")} /> api: REST API (/api/v1)
          </label>
        </Field>
        <Field label="Expires">
          <select className="select w-full" value={days} onChange={(e) => setDays(Number(e.target.value))}>
            <option value={7}>7 days</option>
            <option value={30}>30 days</option>
            <option value={90}>90 days</option>
            <option value={365}>1 year</option>
            <option value={0}>Never</option>
          </select>
        </Field>
        <ErrorAlert error={create.error} className="mt-2" />
      </Modal>

      <Modal open={!!created} onClose={() => setCreated(null)} title="Token created" wide>
        {created && (
          <div className="space-y-4">
            <div className="alert alert-warning alert-soft text-sm">
              <AlertTriangle className="size-4" /> Copy the token now. It is not shown again.
            </div>
            <CopyField value={created.value} />
            {created.token?.scopes.includes("mcp") && <McpSetup token={created.value} />}
          </div>
        )}
      </Modal>

      <Confirm
        open={!!revoke}
        onClose={() => setRevoke(null)}
        title={`Revoke ${revoke?.name}?`}
        danger
        confirmLabel="Revoke"
        busy={del.isPending}
        onConfirm={() => revoke && del.mutate(revoke.id)}
      >
        Clients using this token stop working immediately.
      </Confirm>
    </>
  );
}
