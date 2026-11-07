import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { EllipsisVertical, Monitor, Play, RotateCw, Settings, Square, SquareTerminal, Trash2 } from "lucide-react";
import { api, errorMessage, type Environment } from "../lib/api";
import { Confirm, Spinner, cx } from "./ui";
import { useToast } from "./toast";

/** Start/stop and other quick actions for an environment. */
export function useEnvMutations(env: Environment) {
  const qc = useQueryClient();
  const toast = useToast();
  const opts = {
    onError: (e: unknown) => toast(errorMessage(e), "error"),
    onSettled: () => qc.invalidateQueries({ queryKey: ["environments"] }),
  };
  return {
    start: useMutation({ mutationFn: () => api.startEnvironment(env.name), ...opts }),
    stop: useMutation({ mutationFn: () => api.stopEnvironment(env.name), ...opts }),
    restart: useMutation({ mutationFn: () => api.restartEnvironment(env.name), ...opts, onSuccess: () => toast(`Restarting ${env.name}`) }),
    remove: useMutation({ mutationFn: () => api.deleteEnvironment(env.name), ...opts, onSuccess: () => toast(`Deleting ${env.name}`) }),
  };
}

export function PowerButton({ env, className }: { env: Environment; className?: string }) {
  const m = useEnvMutations(env);
  const busy = m.start.isPending || m.stop.isPending;
  if (env.deleting) return null;
  return env.running ? (
    <button className={cx("btn btn-sm", className)} disabled={busy} onClick={() => m.stop.mutate()} title="Stop">
      {busy ? <Spinner className="loading-xs" /> : <Square className="size-3.5" />} Stop
    </button>
  ) : (
    <button className={cx("btn btn-sm btn-primary", className)} disabled={busy} onClick={() => m.start.mutate()} title="Start">
      {busy ? <Spinner className="loading-xs" /> : <Play className="size-3.5" />} Start
    </button>
  );
}

export function EnvMenu({ env, onDeleted }: { env: Environment; onDeleted?: () => void }) {
  const m = useEnvMutations(env);
  const navigate = useNavigate();
  const [confirm, setConfirm] = useState(false);
  const manage = env.access === "manage";
  return (
    <>
      <div className="dropdown dropdown-end">
        <div tabIndex={0} role="button" className="btn btn-ghost btn-sm btn-square">
          <EllipsisVertical className="size-4" />
        </div>
        <ul tabIndex={0} className="dropdown-content menu z-30 w-52 rounded-box bg-base-100 p-2 shadow-lg">
          <li>
            <button onClick={() => navigate(`/env/${env.name}?open=terminal`)}>
              <SquareTerminal className="size-4" /> New terminal
            </button>
          </li>
          {env.desktop && (
            <li>
              <button onClick={() => navigate(`/env/${env.name}?open=desktop`)}>
                <Monitor className="size-4" /> Desktop
              </button>
            </li>
          )}
          {env.running && (
            <li>
              <button onClick={() => m.restart.mutate()}>
                <RotateCw className="size-4" /> Restart
              </button>
            </li>
          )}
          <li>
            <button onClick={() => navigate(`/env/${env.name}/settings`)}>
              <Settings className="size-4" /> Settings
            </button>
          </li>
          {manage && (
            <li>
              <button className="text-error" onClick={() => setConfirm(true)}>
                <Trash2 className="size-4" /> Delete
              </button>
            </li>
          )}
        </ul>
      </div>
      <Confirm
        open={confirm}
        onClose={() => setConfirm(false)}
        title={`Delete ${env.name}?`}
        danger
        confirmLabel="Delete"
        typeToConfirm={env.name}
        busy={m.remove.isPending}
        onConfirm={() =>
          m.remove.mutate(undefined, {
            onSuccess: () => {
              setConfirm(false);
              onDeleted?.();
            },
          })
        }
      >
        <p>The environment and its home volume are deleted permanently.</p>
      </Confirm>
    </>
  );
}
