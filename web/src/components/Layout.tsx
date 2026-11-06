import { useQuery, useQueryClient } from "@tanstack/react-query";
import { NavLink, Navigate, Outlet, useLocation, useNavigate } from "react-router";
import { api, ApiError } from "../api";

export function Layout() {
  const location = useLocation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const info = useQuery({ queryKey: ["info"], queryFn: api.info, staleTime: Infinity });
  const me = useQuery({ queryKey: ["me"], queryFn: api.me, retry: false });

  if (me.error instanceof ApiError && me.error.status === 401) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }
  if (me.isPending) return <div className="center muted">Loading…</div>;
  if (me.error) return <div className="center error">{me.error.message}</div>;

  const logout = async () => {
    await api.logout();
    qc.clear();
    navigate("/login");
  };

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">devenv</div>
        <nav>
          <NavLink to="/" end>
            Environments
          </NavLink>
          <NavLink to="/images">Images</NavLink>
          <NavLink to="/ssh-keys">SSH keys</NavLink>
        </nav>
        <div className="spacer" />
        <span className="muted small">{me.data?.name}</span>
        {info.data?.authMode === "password" && (
          <button className="link" onClick={logout}>
            Sign out
          </button>
        )}
      </header>
      <main className="content">
        <Outlet />
      </main>
    </div>
  );
}
