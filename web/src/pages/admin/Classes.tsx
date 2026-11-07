import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type AccessInput, type Class } from "../../lib/api";
import { formatBytes, formatCPU } from "../../lib/quantity";
import { PageHeader } from "../../components/Layout";
import { CopyField, ErrorAlert, Field, Loading, Modal, TagInput } from "../../components/ui";
import { AdminOnly } from "./AdminOnly";

function ClassDetail({ cls, onDone }: { cls: Class; onDone: () => void }) {
  const qc = useQueryClient();
  const detail = useQuery({ queryKey: ["class", cls.name], queryFn: () => api.classDetail(cls.name) });
  const users = useQuery({ queryKey: ["users"], queryFn: api.users });
  const [acc, setAcc] = useState<AccessInput>(cls.access);
  const save = useMutation({
    mutationFn: () => api.updateClassAccess(cls.name, acc),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["classes"] });
      onDone();
    },
  });
  return (
    <div className="space-y-3">
      <p className="text-sm text-base-content/60">Storage, network and scheduling are managed declaratively (Helm values or GitOps). Here you choose who may use the class.</p>
      <div className="grid gap-x-4 sm:grid-cols-2">
        <Field label="Groups" hint="Empty users and groups: everyone.">
          <TagInput value={acc.groups} onChange={(g) => setAcc({ ...acc, groups: g })} placeholder="group" />
        </Field>
        <Field label="Users">
          <TagInput value={acc.users} onChange={(u) => setAcc({ ...acc, users: u })} placeholder="user" suggestions={(users.data ?? []).map((u) => ({ value: u.name }))} />
        </Field>
      </div>
      <ErrorAlert error={save.error} />
      <div className="flex justify-end">
        <button className="btn btn-primary btn-sm" onClick={() => save.mutate()} disabled={save.isPending}>
          Save access
        </button>
      </div>
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
          <button key={c.name} className="card border border-base-300 bg-base-100 text-left hover:shadow-md" onClick={() => setSel(c)}>
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
                Max {c.limits.cpu ? formatCPU(c.limits.cpu) : "∞"} · {c.limits.memory ? formatBytes(c.limits.memory) : "∞"} memory · {c.limits.disk ? formatBytes(c.limits.disk) : "∞"} disk
              </div>
              <div className="text-xs text-base-content/50">
                {c.access.groups.length + c.access.users.length === 0 ? "Everyone" : `Only ${[...c.access.groups.map((g) => `@${g}`), ...c.access.users].join(", ")}`}
              </div>
            </div>
          </button>
        ))}
      </div>
      <Modal open={!!sel} onClose={() => setSel(null)} title={`Class ${sel?.name}`} wide>
        {sel && <ClassDetail cls={sel} onDone={() => setSel(null)} />}
      </Modal>
    </AdminOnly>
  );
}
