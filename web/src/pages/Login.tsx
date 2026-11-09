import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { Boxes, KeyRound, LogIn } from "lucide-react";
import { api } from "../lib/api";
import { useInfo } from "../lib/prefs";
import { ErrorAlert, Field, Loading, Spinner } from "../components/ui";

export function LoginPage() {
  const info = useInfo();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(params.get("error") ?? null);
  const ret = params.get("return") || "/";
  const [showPassword, setShowPassword] = useState(false);

  if (info.isLoading) return <Loading />;
  if (info.data?.auth.disabled) {
    navigate(ret, { replace: true });
    return null;
  }
  const providers = info.data?.providers ?? [];
  const passwordOpen = info.data?.auth.password && (providers.length === 0 || showPassword);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.login(username, password);
      qc.clear();
      navigate(ret.startsWith("/") && !ret.startsWith("//") ? ret : "/", { replace: true });
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-h-screen flex-col items-center justify-center gap-6 bg-base-200 bg-[radial-gradient(ellipse_at_top,color-mix(in_oklab,var(--color-primary)_14%,transparent),transparent_60%)] p-4">
      <div className="card w-full max-w-sm border border-base-300 bg-base-100 shadow-lg">
        <div className="card-body gap-4 p-7">
          <div className="flex flex-col items-center gap-2 pb-2">
            <div className="rounded-box bg-primary/10 p-3">
              <Boxes className="size-8 text-primary" />
            </div>
            <h1 className="text-xl font-semibold">Sign in to devenv</h1>
            <p className="text-center text-sm text-base-content/60">Development environments on Kubernetes</p>
          </div>
          {error ? <ErrorAlert error={typeof error === "string" ? new Error(error) : error} /> : null}
          {providers.map((p) => (
            <a key={p.id} className="btn btn-primary w-full" href={`${p.loginURL}?return=${encodeURIComponent(ret)}`}>
              <LogIn className="size-4" /> Continue with {p.name}
            </a>
          ))}
          {info.data?.auth.password && providers.length > 0 && !showPassword && (
            <button className="btn btn-ghost btn-sm" onClick={() => setShowPassword(true)}>
              <KeyRound className="size-4" /> Sign in with the local account
            </button>
          )}
          {passwordOpen && (
            <form onSubmit={submit} className="flex flex-col gap-1">
              {providers.length > 0 && <div className="divider my-1 text-xs">local account</div>}
              <Field label="Username">
                <input className="input w-full" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" />
              </Field>
              <Field label="Password">
                <input
                  className="input w-full"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoComplete="current-password"
                  autoFocus
                />
              </Field>
              <button className={providers.length > 0 ? "btn btn-neutral mt-3 w-full" : "btn btn-primary mt-3 w-full"} disabled={busy}>
                {busy && <Spinner className="loading-xs" />} Sign in
              </button>
            </form>
          )}
          {!info.data?.auth.password && providers.length === 0 && (
            <p className="text-sm text-base-content/60">No login method is configured.</p>
          )}
        </div>
      </div>
      {info.data?.version && <div className="text-xs text-base-content/40">devenv {info.data.version}</div>}
    </div>
  );
}
