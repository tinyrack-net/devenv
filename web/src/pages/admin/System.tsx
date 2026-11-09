import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../../lib/api";
import { formatBytes, formatCPU } from "../../lib/quantity";
import { PageHeader } from "../../components/Layout";
import { Confirm, CopyField, ErrorAlert, Loading } from "../../components/ui";
import { AdminOnly } from "./AdminOnly";

export function AdminSystemPage() {
  const sys = useQuery({ queryKey: ["admin", "system"], queryFn: api.admin.system, refetchInterval: 30_000 });
  const d = sys.data;
  return (
    <AdminOnly>
      <PageHeader title="System" subtitle="Deployment settings come from the configuration file (Helm values) and are read only here." />
      <ErrorAlert error={sys.error} />
      {!d && sys.isLoading && <Loading />}
      {d && (
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-px overflow-hidden rounded-box bg-base-300 sm:grid-cols-5">
            {[
              ["Version", d.version],
              ["Users", d.counts.users ?? 0],
              ["Environments", `${d.counts.running ?? 0} / ${d.counts.environments ?? 0}`],
              ["Images", d.counts.images ?? 0],
              ["Tokens", d.counts.tokens ?? 0],
            ].map(([k, v]) => (
              <div key={k as string} className="bg-base-100 p-4">
                <div className="text-xs text-base-content/60">{k}</div>
                <div className="mt-1 text-xl font-semibold break-all">{v}</div>
              </div>
            ))}
          </div>
          <section className="min-w-0 rounded-box bg-base-100 p-4 sm:p-5">
            <h2 className="mb-3 font-semibold">Nodes</h2>
            <div className="overflow-x-auto">
              <table className="table table-sm table-stack">
                <thead>
                  <tr>
                    <th>Node</th>
                    <th>Free CPU</th>
                    <th>Free memory</th>
                    <th>Allocatable</th>
                  </tr>
                </thead>
                <tbody>
                  {d.nodes.map((n) => (
                    <tr key={n.name}>
                      <td className="font-medium">{n.name}</td>
                      <td data-label="Free CPU">{formatCPU(n.free.cpu)}</td>
                      <td data-label="Free memory">{formatBytes(n.free.memory)}</td>
                      <td data-label="Allocatable" className="text-base-content/60">
                        {formatCPU(n.allocatable.cpu)} · {formatBytes(n.allocatable.memory)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
          <section className="min-w-0 rounded-box bg-base-100 p-4 sm:p-5">
            <h2 className="mb-3 font-semibold">SSH gateway</h2>
            <dl className="grid grid-cols-1 gap-y-1 text-sm sm:grid-cols-[8rem_1fr]">
              <dt className="text-base-content/60">Address</dt>
              <dd>{d.ssh.enabled ? `${d.ssh.host || window.location.hostname}:${d.ssh.port}` : "disabled"}</dd>
              <dt className="text-base-content/60">Fingerprint</dt>
              <dd className="font-mono-tight break-all">{d.ssh.fingerprint}</dd>
              <dt className="text-base-content/60">Helper image</dt>
              <dd className="font-mono-tight break-all">{d.helperImage || "disabled"}</dd>
              <dt className="text-base-content/60">Namespace</dt>
              <dd>{d.namespace}</dd>
              <dt className="text-base-content/60">Database</dt>
              <dd>{d.database}</dd>
            </dl>
          </section>
          <Orphans />
          <section className="min-w-0 rounded-box bg-base-100 p-4 sm:p-5">
            <h2 className="mb-3 font-semibold">Configuration</h2>
            <CopyField value={JSON.stringify(d.config, null, 2)} />
          </section>
        </div>
      )}
    </AdminOnly>
  );
}

/** Kubernetes objects of environments the database does not know. */
function Orphans() {
  const qc = useQueryClient();
  const orphans = useQuery({ queryKey: ["admin", "orphans"], queryFn: api.orphans });
  const [target, setTarget] = useState<string | null>(null);
  const del = useMutation({
    mutationFn: (name: string) => api.deleteOrphan(name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "orphans"] });
      setTarget(null);
    },
  });
  if (!orphans.data?.length) return null;
  return (
    <section className="min-w-0 rounded-box bg-base-100 p-4 sm:p-5">
      <h2 className="mb-1 font-semibold">Orphaned objects</h2>
      <p className="mb-3 text-sm text-base-content/60">
        Objects of environments without a database record, for example after restoring the database to an earlier point. They are never deleted automatically.
        Create an environment with the same name to adopt the home volume, or delete them.
      </p>
      <ErrorAlert error={del.error} />
      <table className="table table-sm table-stack">
        <tbody>
          {orphans.data.map((o) => (
            <tr key={o.name}>
              <td className="font-medium">{o.name}</td>
              <td className="font-mono-tight text-xs text-base-content/70">{o.objects.join(", ")}</td>
              <td className="text-right">
                <button className="btn btn-ghost btn-xs text-error" onClick={() => setTarget(o.name)}>
                  Delete
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <Confirm
        open={!!target}
        onClose={() => setTarget(null)}
        title={`Delete the objects of ${target}, including its home volume?`}
        danger
        confirmLabel="Delete"
        busy={del.isPending}
        onConfirm={() => target && del.mutate(target)}
      />
    </section>
  );
}
