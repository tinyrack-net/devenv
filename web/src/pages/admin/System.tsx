import { useQuery } from "@tanstack/react-query";
import { api } from "../../lib/api";
import { formatBytes, formatCPU } from "../../lib/quantity";
import { PageHeader } from "../../components/Layout";
import { CopyField, ErrorAlert, Loading } from "../../components/ui";
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
            </dl>
          </section>
          <section className="min-w-0 rounded-box bg-base-100 p-4 sm:p-5">
            <h2 className="mb-3 font-semibold">Configuration</h2>
            <CopyField value={JSON.stringify(d.config, null, 2)} />
          </section>
        </div>
      )}
    </AdminOnly>
  );
}
