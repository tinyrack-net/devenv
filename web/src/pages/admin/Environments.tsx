import { useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { Server } from "lucide-react";
import { api } from "../../lib/api";
import { PageHeader } from "../../components/Layout";
import { Empty, ErrorAlert, Loading } from "../../components/ui";
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
      {envs.data?.length === 0 && <Empty icon={<Server className="size-10" />} title="No environments" />}
      {!!envs.data?.length && <EnvTable envs={envs.data} showOwner />}
    </AdminOnly>
  );
}
