import { useCallback, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot } from "lucide-react";
import { api, type Entry } from "../../lib/api";
import { useActivity } from "../../lib/stream";
import { PageHeader } from "../../components/Layout";
import { Empty, ErrorAlert, Loading } from "../../components/ui";
import { ActivityList } from "../../workspace/ActivityPanel";
import { AdminOnly } from "./AdminOnly";

export function AdminActivityPage() {
  const qc = useQueryClient();
  const [env, setEnv] = useState("");
  const list = useQuery({ queryKey: ["admin", "activity", env], queryFn: () => api.admin.activity(env || undefined) });
  const onEntry = useCallback(
    (e: Entry) => {
      if (env && e.environment !== env) return;
      qc.setQueryData<Entry[]>(["admin", "activity", env], (l) => mergeEntry(l, e));
    },
    [qc, env],
  );
  useActivity(onEntry);
  return (
    <AdminOnly>
      <PageHeader
        title="Agent activity"
        subtitle="MCP tool calls since the server started."
        actions={<input className="input input-sm" placeholder="Filter by environment" value={env} onChange={(e) => setEnv(e.target.value)} />}
      />
      <ErrorAlert error={list.error} />
      {list.isLoading && <Loading />}
      {list.data?.length === 0 && <Empty icon={<Bot className="size-10" />} title="No agent activity yet" />}
      {!!list.data?.length && (
        <div className="rounded-box bg-base-100">
          <ActivityList entries={list.data} showEnv />
        </div>
      )}
    </AdminOnly>
  );
}

export function mergeEntry(list: Entry[] | undefined, e: Entry): Entry[] {
  const l = list ?? [];
  const i = l.findIndex((x) => x.id === e.id);
  if (i >= 0) {
    const out = l.slice();
    out[i] = e;
    return out;
  }
  return [e, ...l].slice(0, 500);
}
