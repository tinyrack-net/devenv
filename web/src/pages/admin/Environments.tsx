import { useState } from "react";
import { useSearchParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { HardDrive, Server } from "lucide-react";
import { api, type Environment } from "../../lib/api";
import { PageHeader } from "../../components/Layout";
import { Confirm, Empty, ErrorAlert, Loading } from "../../components/ui";
import { EnvTable } from "../Environments";
import { AdminOnly } from "./AdminOnly";

export function AdminEnvironmentsPage() {
  const [params, setParams] = useSearchParams();
  const owner = params.get("owner") ?? "";
  const users = useQuery({ queryKey: ["users"], queryFn: api.users });
  const envs = useQuery({ queryKey: ["environments", { all: true, owner }], queryFn: () => api.environments({ all: true, owner: owner || undefined }) });
  return (
    <AdminOnly>
      <PageHeader
        title="All environments"
        actions={
          <select className="select select-sm" value={owner} onChange={(e) => setParams(e.target.value ? { owner: e.target.value } : {})}>
            <option value="">All users</option>
            {users.data?.map((u) => (
              <option key={u.name} value={u.name}>
                {u.name}
              </option>
            ))}
          </select>
        }
      />
      <ErrorAlert error={envs.error} />
      {envs.isLoading && <Loading />}
      <AwaitingVolumes envs={envs.data ?? []} />
      {envs.data?.length === 0 && <Empty icon={<Server className="size-10" />} title="No environments" />}
      {!!envs.data?.length && <EnvTable envs={envs.data} showOwner />}
    </AdminOnly>
  );
}

/** Environments waiting for a restored home volume, with a way out. */
function AwaitingVolumes({ envs }: { envs: Environment[] }) {
  const waiting = envs.filter((e) => e.reason === "AwaitingVolume");
  const qc = useQueryClient();
  const [target, setTarget] = useState<string | null>(null);
  const reset = useMutation({
    mutationFn: (name: string) => api.resetVolume(name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["environments"] });
      setTarget(null);
    },
  });
  if (waiting.length === 0) return null;
  return (
    <div role="alert" className="alert alert-warning alert-soft mb-4 items-start">
      <HardDrive className="size-5" />
      <div className="space-y-2">
        <div className="font-medium">Waiting for home volumes</div>
        <p className="text-sm">
          These environments had a home volume that is not there (for example while a storage restore runs). They start as soon as it is back. Reset the volume
          only if it is lost for good: the environment then starts with an empty home directory.
        </p>
        <div className="flex flex-wrap gap-2">
          {waiting.map((e) => (
            <button key={e.name} className="btn btn-xs" onClick={() => setTarget(e.name)}>
              Reset {e.name}
            </button>
          ))}
        </div>
        <ErrorAlert error={reset.error} />
      </div>
      <Confirm
        open={!!target}
        onClose={() => setTarget(null)}
        title={`Start ${target} with an empty home volume?`}
        danger
        confirmLabel="Reset volume"
        busy={reset.isPending}
        onConfirm={() => target && reset.mutate(target)}
      />
    </div>
  );
}
