import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type Environment } from "../api";
import { ErrorBox, timeAgo } from "./common";

const presets = ["", "30m", "1h", "4h", "12h", "24h"];

/** Shows and edits the idle auto-stop timeout. */
export function IdleControl({ env }: { env: Environment }) {
  const qc = useQueryClient();
  const current = normalize(env.idleTimeout);
  const [value, setValue] = useState(current);
  const save = useMutation({
    mutationFn: (idleTimeout: string) => api.updateEnvironment(env.name, { idleTimeout }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["environments"] }),
  });
  const options = presets.includes(current) ? presets : [...presets, current];
  return (
    <div className="connect">
      <ErrorBox error={save.error} />
      <div className="row">
        <select value={value} onChange={(e) => setValue(e.target.value)} style={{ width: 160, flex: "none" }}>
          {options.map((p) => (
            <option key={p} value={p}>
              {p === "" ? "Never" : `After ${p} idle`}
            </option>
          ))}
        </select>
        <button className="small" disabled={value === current || save.isPending} onClick={() => save.mutate(value)}>
          Save
        </button>
      </div>
      <div className="small muted">
        Last activity: {env.lastActivity ? timeAgo(env.lastActivity) : "none recorded"}. Activity is the web terminal,
        desktop, SSH gateway sessions and MCP calls; direct SSH to a dedicated IP is not seen.
      </div>
    </div>
  );
}

/** "1h0m0s" -> "1h", "30m0s" -> "30m". */
function normalize(d?: string): string {
  if (!d) return "";
  return d.replace(/(\d+[hm])0s$/, "$1").replace(/^(\d+h)0m$/, "$1");
}
