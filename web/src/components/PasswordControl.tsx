import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type Environment } from "../api";
import { ErrorBox } from "./common";

/** Set, change or remove the dev user's password. */
export function PasswordControl({ env }: { env: Environment }) {
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const save = useMutation({
    mutationFn: (password: string) => api.updateEnvironment(env.name, { password }),
    onSuccess: () => {
      setEditing(false);
      setPw("");
      setPw2("");
      qc.invalidateQueries({ queryKey: ["environments"] });
    },
  });
  const error = pw && pw.length < 8 ? "At least 8 characters" : pw && pw !== pw2 ? "Passwords do not match" : null;

  return (
    <div className="connect">
      <ErrorBox error={save.error} />
      <div className="small">
        Password login: <strong>{env.passwordSet ? "enabled" : "disabled"}</strong>
      </div>
      {!editing ? (
        <div className="row">
          <button className="small" onClick={() => setEditing(true)}>
            {env.passwordSet ? "Change password" : "Set password"}
          </button>
          {env.passwordSet && (
            <button
              className="small danger"
              disabled={save.isPending}
              onClick={() => confirm("Disable password login?") && save.mutate("")}
            >
              Remove password
            </button>
          )}
        </div>
      ) : (
        <>
          <div className="row">
            <input type="password" autoComplete="new-password" placeholder="New password" value={pw} onChange={(e) => setPw(e.target.value)} />
            <input type="password" autoComplete="new-password" placeholder="Confirm" value={pw2} onChange={(e) => setPw2(e.target.value)} />
          </div>
          {error && <div className="small error">{error}</div>}
          <div className="row">
            <button className="small" onClick={() => setEditing(false)}>
              Cancel
            </button>
            <button className="small primary" disabled={!pw || !!error || save.isPending} onClick={() => save.mutate(pw)}>
              Save
            </button>
          </div>
          <div className="small muted">Applied within a few seconds without restarting.</div>
        </>
      )}
    </div>
  );
}
