import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type Environment, type PortSpec } from "../api";
import { CopyButton, ErrorBox } from "./common";

/** Ports exposed on the environment's LoadBalancer address. */
export function Ports({ env }: { env: Environment }) {
  const qc = useQueryClient();
  const [port, setPort] = useState("");
  const [protocol, setProtocol] = useState<"TCP" | "UDP">("TCP");
  const save = useMutation({
    mutationFn: (ports: PortSpec[]) => api.updateEnvironment(env.name, { ports }),
    onSuccess: () => {
      setPort("");
      qc.invalidateQueries({ queryKey: ["environments"] });
    },
  });
  const add = () => {
    const n = Number(port);
    if (!Number.isInteger(n) || n < 1 || n > 65535) return;
    save.mutate([...env.ports, { port: n, protocol }]);
  };
  const remove = (p: PortSpec) =>
    save.mutate(env.ports.filter((x) => !(x.port === p.port && (x.protocol ?? "TCP") === (p.protocol ?? "TCP"))));

  return (
    <div className="connect">
      <ErrorBox error={save.error} />
      <table className="table compact">
        <tbody>
          <tr>
            <td className="mono">22/TCP</td>
            <td className="small muted">SSH (always)</td>
            <td />
          </tr>
          {env.ports.map((p) => {
            const addr = env.ip ? `${env.ip}:${p.port}` : "";
            return (
              <tr key={`${p.protocol}/${p.port}`}>
                <td className="mono">
                  {p.port}/{p.protocol ?? "TCP"}
                </td>
                <td className="small muted">{addr && <CopyButton text={addr} label={addr} />}</td>
                <td className="actions">
                  <button className="small danger" disabled={save.isPending} onClick={() => remove(p)}>
                    Remove
                  </button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <div className="row">
        <input
          type="number"
          min={1}
          max={65535}
          placeholder="Port"
          value={port}
          onChange={(e) => setPort(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && add()}
          style={{ width: 120, flex: "none" }}
        />
        <select value={protocol} onChange={(e) => setProtocol(e.target.value as "TCP" | "UDP")} style={{ width: 90, flex: "none" }}>
          <option>TCP</option>
          <option>UDP</option>
        </select>
        <button disabled={!port || save.isPending} onClick={add}>
          Expose
        </button>
      </div>
    </div>
  );
}
