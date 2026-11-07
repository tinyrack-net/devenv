import { useEffect, useSyncExternalStore } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { Entry, Environment } from "./api";

// One EventSource per tab keeps environment lists and details fresh without
// polling, and feeds the agent activity panels.

type Listener = (e: Entry) => void;
const activityListeners = new Set<Listener>();
let status: "connecting" | "open" | "closed" = "connecting";
const statusListeners = new Set<() => void>();

function setStatus(s: typeof status) {
  status = s;
  statusListeners.forEach((l) => l());
}

function upsertList(list: Environment[] | undefined, env: Environment): Environment[] | undefined {
  if (!list) return list;
  const i = list.findIndex((x) => x.name === env.name);
  if (i < 0) return [...list, env].sort((a, b) => a.name.localeCompare(b.name));
  const out = list.slice();
  out[i] = env;
  return out;
}

function connect(qc: QueryClient): () => void {
  let es: EventSource | null = null;
  let closed = false;
  let retry: ReturnType<typeof setTimeout> | undefined;
  const open = () => {
    if (closed) return;
    setStatus("connecting");
    es = new EventSource("/api/v1/stream", { withCredentials: true });
    es.onopen = () => {
      setStatus("open");
      // Resync after (re)connecting.
      qc.invalidateQueries({ queryKey: ["environments"] });
    };
    es.addEventListener("environment", (ev) => {
      const msg = JSON.parse((ev as MessageEvent).data) as
        | { type: "upsert"; environment: Environment }
        | { type: "delete"; name: string };
      if (msg.type === "upsert") {
        const env = msg.environment;
        qc.setQueryData<Environment>(["environment", env.name], env);
        qc.setQueriesData<Environment[]>({ queryKey: ["environments"] }, (l) => upsertList(l, env));
      } else {
        qc.removeQueries({ queryKey: ["environment", msg.name], exact: true });
        qc.setQueriesData<Environment[]>({ queryKey: ["environments"] }, (l) => l?.filter((x) => x.name !== msg.name));
      }
    });
    es.addEventListener("activity", (ev) => {
      const entry = JSON.parse((ev as MessageEvent).data) as Entry;
      activityListeners.forEach((l) => l(entry));
    });
    es.onerror = () => {
      setStatus("closed");
      es?.close();
      retry = setTimeout(open, 3000);
    };
  };
  open();
  return () => {
    closed = true;
    clearTimeout(retry);
    es?.close();
  };
}

/** Mount once inside the authenticated layout. */
export function useEventStream() {
  const qc = useQueryClient();
  useEffect(() => connect(qc), [qc]);
}

export function useStreamStatus() {
  return useSyncExternalStore(
    (cb) => {
      statusListeners.add(cb);
      return () => statusListeners.delete(cb);
    },
    () => status,
  );
}

export function useActivity(listener: Listener) {
  useEffect(() => {
    activityListeners.add(listener);
    return () => {
      activityListeners.delete(listener);
    };
  }, [listener]);
}
