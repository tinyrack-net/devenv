import { useCallback, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, ChevronRight, Loader2 } from "lucide-react";
import { api, type Entry } from "../lib/api";
import { useActivity } from "../lib/stream";
import { cx, timeAgo } from "../components/ui";

function EntryRow({ e, showEnv }: { e: Entry; showEnv?: boolean }) {
  const [open, setOpen] = useState(false);
  const hasDetail = !!(e.detail || e.error);
  return (
    <li className="border-b border-base-300 last:border-0">
      <button className="flex w-full items-start gap-2 px-3 py-2 text-left hover:bg-base-200/60" onClick={() => hasDetail && setOpen((o) => !o)}>
        <ChevronRight className={cx("mt-0.5 size-3.5 shrink-0 transition-transform", open && "rotate-90", !hasDetail && "opacity-0")} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 text-xs">
            <span className={cx("badge badge-xs", e.error ? "badge-error" : e.running ? "badge-info" : "badge-ghost")}>{e.tool}</span>
            {e.running && <Loader2 className="size-3 animate-spin" />}
            {showEnv && e.environment && <span className="font-medium">{e.environment}</span>}
            <span className="ml-auto shrink-0 text-base-content/50" title={new Date(e.time).toLocaleString()}>
              {timeAgo(e.time)}
              {!e.running && e.durationMs > 0 && ` · ${e.durationMs < 1000 ? `${e.durationMs}ms` : `${(e.durationMs / 1000).toFixed(1)}s`}`}
            </span>
          </div>
          {e.summary && <div className="font-mono-tight mt-1 truncate">{e.summary}</div>}
          <div className="mt-0.5 text-[11px] text-base-content/50">
            {e.user}
            {e.token && ` · ${e.token}`}
          </div>
        </div>
      </button>
      {open && (
        <pre className="font-mono-tight mx-3 mb-2 max-h-72 overflow-auto rounded-field bg-base-200 p-2 whitespace-pre-wrap">
          {e.error && <span className="text-error">{e.error}\n</span>}
          {e.detail}
        </pre>
      )}
    </li>
  );
}

export function ActivityList({ entries, showEnv }: { entries: Entry[]; showEnv?: boolean }) {
  return (
    <ul>
      {entries.map((e) => (
        <EntryRow key={e.id} e={e} showEnv={showEnv} />
      ))}
    </ul>
  );
}

/** Live log of agent tool calls on this environment. */
export function ActivityPanel({ env }: { env: string }) {
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ["activity", env], queryFn: () => api.activity(env) });
  const onEntry = useCallback(
    (e: Entry) => {
      if (e.environment !== env) return;
      qc.setQueryData<Entry[]>(["activity", env], (l) => {
        const cur = l ?? [];
        const i = cur.findIndex((x) => x.id === e.id);
        if (i >= 0) {
          const out = cur.slice();
          out[i] = e;
          return out;
        }
        return [e, ...cur].slice(0, 300);
      });
    },
    [qc, env],
  );
  useActivity(onEntry);
  if (!list.data?.length)
    return (
      <div className="flex flex-col items-center gap-2 p-6 text-center text-sm text-base-content/60">
        <Bot className="size-8 opacity-50" />
        No agent activity yet. Tool calls from AI agents (MCP) on this environment appear here live.
      </div>
    );
  return <ActivityList entries={list.data} />;
}

/** Count of calls in progress, for the activity tab badge. */
export function useRunningAgentCalls(env: string): number {
  const list = useQuery({ queryKey: ["activity", env], queryFn: () => api.activity(env), staleTime: 10_000 });
  return (list.data ?? []).filter((e) => e.running).length;
}
