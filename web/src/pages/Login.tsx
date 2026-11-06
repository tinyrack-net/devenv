import { useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLocation, useNavigate } from "react-router";
import { api } from "../api";
import { ErrorBox, Field } from "../components/common";

export function LoginPage() {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const navigate = useNavigate();
  const location = useLocation();
  const qc = useQueryClient();

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.login(username, password);
      await qc.invalidateQueries();
      const from = (location.state as { from?: string } | null)?.from ?? "/";
      navigate(from, { replace: true });
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login">
      <form className="card login-card" onSubmit={submit}>
        <h1>devenv</h1>
        <ErrorBox error={error} />
        <Field label="Username">
          <input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" />
        </Field>
        <Field label="Password">
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
            autoFocus
          />
        </Field>
        <button className="primary" disabled={busy}>
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
