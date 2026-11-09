import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, type Class } from "../../lib/api";
import { formatBytes, formatCPU } from "../../lib/quantity";
import { PageHeader } from "../../components/Layout";
import { CopyField, ErrorAlert, Loading, Modal } from "../../components/ui";
import { AdminOnly } from "./AdminOnly";

function ClassDetail({ cls }: { cls: Class }) {
  const detail = useQuery({ queryKey: ["class", cls.name], queryFn: () => api.classDetail(cls.name) });
  const who = cls.access.groups.length + cls.access.users.length === 0 ? "everyone" : [...cls.access.groups.map((g) => `@${g}`), ...cls.access.users].join(", ");
  return (
    <div className="space-y-3">
      <p className="text-sm text-base-content/60">
        Classes are part of the deployment configuration (Helm values <code>defaultClass</code> / <code>extraClasses</code>). Available to {who}.
      </p>
      <h3 className="text-sm font-medium">Spec</h3>
      {detail.data ? <CopyField value={JSON.stringify(detail.data.spec, null, 2)} /> : <Loading />}
    </div>
  );
}

export function AdminClassesPage() {
  const classes = useQuery({ queryKey: ["classes"], queryFn: api.classes });
  const [sel, setSel] = useState<Class | null>(null);
  return (
    <AdminOnly>
      <PageHeader title="Classes" subtitle="How environments run on this cluster: storage, network and limits." />
      <ErrorAlert error={classes.error} />
      {classes.isLoading && <Loading />}
      <div className="grid gap-4 md:grid-cols-2">
        {classes.data?.map((c) => (
          <button key={c.name} className="card border border-base-300 bg-base-100 text-left transition hover:border-primary/40 hover:shadow-sm" onClick={() => setSel(c)}>
            <div className="card-body gap-2 p-5">
              <div className="flex items-center gap-2 text-lg font-semibold">
                {c.name}
                {c.default && <span className="badge badge-primary badge-soft badge-sm">default</span>}
              </div>
              <div className="flex flex-wrap gap-1">
                <span className="badge badge-sm">network: {c.networkMode}</span>
                <span className="badge badge-sm">storage: {c.storageMode}</span>
                {c.storageClass && <span className="badge badge-sm">{c.storageClass}</span>}
              </div>
              <div className="text-sm text-base-content/70">
                {c.limits.cpu || c.limits.memory || c.limits.disk
                  ? `Up to ${[c.limits.cpu && formatCPU(c.limits.cpu), c.limits.memory && `${formatBytes(c.limits.memory)} memory`, c.limits.disk && `${formatBytes(c.limits.disk)} disk`].filter(Boolean).join(" · ")} per environment`
                  : "No per-environment limits"}
              </div>
              <div className="text-xs text-base-content/50">
                {c.access.groups.length + c.access.users.length === 0 ? "Everyone" : `Only ${[...c.access.groups.map((g) => `@${g}`), ...c.access.users].join(", ")}`}
              </div>
            </div>
          </button>
        ))}
      </div>
      <Modal open={!!sel} onClose={() => setSel(null)} title={`Class ${sel?.name}`} wide>
        {sel && <ClassDetail cls={sel} />}
      </Modal>
    </AdminOnly>
  );
}
