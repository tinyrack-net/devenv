// Typed client for the devenv REST API. Types are generated from Go
// (`make types` → src/gen).
import type * as T from "../gen/api";

export type * from "../gen/api";


export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function request<R>(method: string, path: string, body?: unknown): Promise<R> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers: {
      "X-Requested-With": "devenv",
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: "same-origin",
  });
  if (res.status === 401 && !path.startsWith("/login")) {
    const here = window.location.pathname + window.location.search;
    if (window.location.pathname !== "/login") {
      window.location.assign(`/login?return=${encodeURIComponent(here)}`);
    }
    throw new ApiError(401, "unauthorized");
  }
  if (!res.ok) {
    let msg = res.statusText;
    try {
      msg = (await res.json()).error ?? msg;
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, msg);
  }
  if (res.status === 204 || res.headers.get("Content-Length") === "0") return undefined as R;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as R;
}

const e = encodeURIComponent;

export const api = {
  info: () => request<T.Info>("GET", "/info"),
  login: (username: string, password: string) => request<unknown>("POST", "/login", { username, password }),
  logout: () => request<void>("POST", "/logout"),

  me: () => request<T.Me>("GET", "/me"),
  updateMe: (r: T.UpdateMeRequest) => request<T.Me>("PATCH", "/me", r),
  myUsage: () => request<T.Usage>("GET", "/me/usage"),

  images: (all = false) => request<T.Image[]>("GET", `/images${all ? "?all=1" : ""}`),
  createImage: (i: T.ImageInput) => request<T.Image>("POST", "/images", i),
  updateImage: (name: string, i: T.ImageInput) => request<T.Image>("PUT", `/images/${e(name)}`, i),
  deleteImage: (name: string) => request<void>("DELETE", `/images/${e(name)}`),

  classes: () => request<T.Class[]>("GET", "/classes"),
  classDetail: (name: string) => request<T.ClassDetail>("GET", `/classes/${e(name)}`),
  orphans: () => request<T.Orphan[]>("GET", "/admin/orphans"),
  deleteOrphan: (name: string) => request<void>("DELETE", `/admin/orphans/${e(name)}`),
  resetVolume: (name: string) => request<T.Environment>("POST", `/admin/environments/${e(name)}/reset-volume`),
  capacity: (cls: string, exclude?: string) =>
    request<T.Capacity>("GET", `/classes/${e(cls)}/capacity${exclude ? `?exclude=${e(exclude)}` : ""}`),

  environments: (opts: { all?: boolean; owner?: string } = {}) => {
    const q = new URLSearchParams();
    if (opts.all) q.set("all", "1");
    if (opts.owner) q.set("owner", opts.owner);
    const s = q.toString();
    return request<T.Environment[]>("GET", `/environments${s ? `?${s}` : ""}`);
  },
  environment: (name: string) => request<T.Environment>("GET", `/environments/${e(name)}`),
  createEnvironment: (r: T.CreateEnvironmentRequest) => request<T.Environment>("POST", "/environments", r),
  updateEnvironment: (name: string, r: T.UpdateEnvironmentRequest) =>
    request<T.Environment>("PATCH", `/environments/${e(name)}`, r),
  deleteEnvironment: (name: string) => request<void>("DELETE", `/environments/${e(name)}`),
  startEnvironment: (name: string) => request<T.Environment>("POST", `/environments/${e(name)}/start`),
  stopEnvironment: (name: string) => request<T.Environment>("POST", `/environments/${e(name)}/stop`),
  restartEnvironment: (name: string) => request<T.Environment>("POST", `/environments/${e(name)}/restart`),
  events: (name: string) => request<T.Event[]>("GET", `/environments/${e(name)}/events`),
  activity: (name: string) => request<T.Entry[]>("GET", `/environments/${e(name)}/activity`),

  sessions: (name: string) => request<T.PtySession[]>("GET", `/environments/${e(name)}/sessions`),
  renameSession: (name: string, id: string, newName: string) =>
    request<T.PtySession>("PATCH", `/environments/${e(name)}/sessions/${e(id)}`, { name: newName }),
  killSession: (name: string, id: string) => request<void>("DELETE", `/environments/${e(name)}/sessions/${e(id)}`),
  processes: (name: string) => request<T.Process[]>("GET", `/environments/${e(name)}/processes`),
  processOutput: (name: string, id: string, offset: number, wait = 0) =>
    request<T.Output>("GET", `/environments/${e(name)}/processes/${e(id)}/output?offset=${offset}&wait=${wait}`),
  startProcess: (name: string, r: { id: string; command: string; cwd?: string }) =>
    request<T.Output>("POST", `/environments/${e(name)}/processes`, r),
  stopProcess: (name: string, id: string, remove = false) =>
    request<T.Output>("POST", `/environments/${e(name)}/processes/${e(id)}/stop${remove ? "?remove=1" : ""}`),
  ports: (name: string) => request<T.Port[]>("GET", `/environments/${e(name)}/ports`),
  stats: (name: string) => request<T.StatsResult>("GET", `/environments/${e(name)}/stats`),

  sshKeys: () => request<T.SSHKey[]>("GET", "/sshkeys"),
  addSSHKey: (name: string, publicKey: string) => request<T.SSHKey>("POST", "/sshkeys", { name, publicKey }),
  deleteSSHKey: (id: string) => request<void>("DELETE", `/sshkeys/${e(id)}`),

  tokens: () => request<T.Token[]>("GET", "/tokens"),
  createToken: (r: T.CreateTokenRequest) => request<T.CreatedToken>("POST", "/tokens", r),
  deleteToken: (id: string) => request<void>("DELETE", `/tokens/${e(id)}`),

  users: () => request<T.UserSummary[]>("GET", "/users"),

  admin: {
    users: () => request<T.User[]>("GET", "/users?detail=1&usage=1"),
    user: (name: string) => request<T.User>("GET", `/admin/users/${e(name)}`),
    createUser: (r: T.CreateUserRequest) => request<T.User>("POST", "/admin/users", r),
    updateUser: (name: string, r: T.UpdateUserRequest) => request<T.User>("PATCH", `/admin/users/${e(name)}`, r),
    deleteUser: (name: string, purge: boolean) => request<void>("DELETE", `/admin/users/${e(name)}${purge ? "?purge=1" : ""}`),
    tokens: (user?: string) => request<T.Token[]>("GET", `/admin/tokens${user ? `?user=${e(user)}` : ""}`),
    deleteToken: (id: string) => request<void>("DELETE", `/admin/tokens/${e(id)}`),
    activity: (env?: string) => request<T.Entry[]>("GET", `/admin/activity${env ? `?environment=${e(env)}` : ""}`),
    system: () => request<T.System>("GET", "/admin/system"),
  },
};

/** Builds a same-origin WebSocket URL for an API path. */
export function wsURL(path: string): string {
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}/api/v1${path}`;
}

export function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message;
  return String(err);
}

export function isTransitional(phase: string, deleting?: boolean): boolean {
  return deleting || phase === "Pending" || phase === "Starting" || phase === "Stopping";
}
