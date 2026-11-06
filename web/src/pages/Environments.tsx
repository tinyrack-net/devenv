import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router";
import { api, type Environment } from "../api";
import { ErrorBox, PhaseBadge, isTransitional, timeAgo } from "../components/common";
import { formatBytes, formatCPU } from "../quantity";

export function EnvironmentsPage() {
  const envs = useQuery({
    queryKey: ["environments"],
    queryFn: api.environments,
    refetchInterval: (q) => (q.state.data?.some((e) => isTransitional(e.phase, e.deleting)) ? 2000 : 10000),
  });

  return (
    <>
      <div className="page-header">
        <h1>Environments</h1>
        <Link to="/environments/new" className="button primary">
          New environment
        </Link>
      </div>
      <ErrorBox error={envs.error} />
      {envs.data && envs.data.length === 0 && (
        <div className="card empty">
          <p>No environments yet.</p>
          <Link to="/environments/new" className="button primary">
            Create your first environment
          </Link>
        </div>
      )}
      {envs.data && envs.data.length > 0 && (
        <table className="table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Status</th>
              <th>Image</th>
              <th>Resources</th>
              <th>Created</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {envs.data.map((e) => (
              <Row key={e.name} env={e} />
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

function Row({ env }: { env: Environment }) {
  const qc = useQueryClient();
  const toggle = useMutation({
    mutationFn: () => (env.running ? api.stopEnvironment(env.name) : api.startEnvironment(env.name)),
    onSettled: () => qc.invalidateQueries({ queryKey: ["environments"] }),
    onError: (err) => alert(err.message),
  });
  return (
    <tr>
      <td>
        <Link to={`/environments/${env.name}`}>
          <strong>{env.name}</strong>
        </Link>
      </td>
      <td>
        <PhaseBadge phase={env.phase} deleting={env.deleting} />
        {env.message && env.phase === "Failed" && <div className="small muted truncate">{env.message}</div>}
      </td>
      <td>
        {env.image}
        {env.desktop && <span className="tag">desktop</span>}
      </td>
      <td className="small">
        {env.resources.cpu ? formatCPU(env.resources.cpu) : "default"} ·{" "}
        {env.resources.memory ? formatBytes(env.resources.memory) : "default"}
      </td>
      <td className="small muted">{timeAgo(env.createdAt)}</td>
      <td className="actions">
        {!env.deleting && (
          <button className="small" disabled={toggle.isPending} onClick={() => toggle.mutate()}>
            {env.running ? "Stop" : "Start"}
          </button>
        )}
      </td>
    </tr>
  );
}
