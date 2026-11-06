// Typed client for the devenv REST API.

export interface Resources {
  cpu?: string;
  memory?: string;
  disk?: string;
}

export interface Info {
  authMode: "password" | "none";
  namespace: string;
  version: string;
  ssh: {
    enabled: boolean;
    host?: string;
    port: number;
    hostKey?: string;
    fingerprint?: string;
  };
}

export interface User {
  name: string;
  admin: boolean;
}

export interface Image {
  name: string;
  image: string;
  displayName?: string;
  description?: string;
  desktop: boolean;
  disabled: boolean;
  imagePullPolicy?: string;
  defaultResources: Resources;
  createdAt: string;
  inUse: number;
}

export type ImageInput = Omit<Image, "createdAt" | "inUse">;

export interface Class {
  name: string;
  storageMode: "persistent" | "ephemeral";
  storageClass?: string;
  defaultDisk?: string;
  networkMode: "gateway" | "loadBalancer" | "multus";
  limits: Resources;
  default: boolean;
}

export interface NodeCapacity {
  name: string;
  allocatable: { cpu: string; memory: string };
  allocated: { cpu: string; memory: string };
  free: { cpu: string; memory: string };
}

export interface Capacity {
  class: string;
  maxCPU: string;
  maxMemory: string;
  maxDisk?: string;
  nodes: NodeCapacity[];
}

export type Phase = "Pending" | "Starting" | "Running" | "Stopping" | "Stopped" | "Failed";

export interface Environment {
  name: string;
  owner?: string;
  image: string;
  imageRef?: string;
  class: string;
  resources: Resources;
  running: boolean;
  phase: Phase;
  message?: string;
  desktop: boolean;
  nodeName?: string;
  ip?: string;
  mac?: string;
  networkMode?: "gateway" | "loadBalancer" | "multus";
  ports: PortSpec[];
  sshAuthorizedKeys: string[];
  createdAt: string;
  deleting: boolean;
  serviceName?: string;
}

export interface PortSpec {
  port: number;
  protocol?: "TCP" | "UDP";
  name?: string;
}

export interface CreateEnvironment {
  name: string;
  image: string;
  class?: string;
  resources: Resources;
  sshAuthorizedKeys?: string[];
  running?: boolean;
  ports?: PortSpec[];
}

export interface UpdateEnvironment {
  image?: string;
  resources?: Resources;
  sshAuthorizedKeys?: string[];
  running?: boolean;
  ports?: PortSpec[];
}

export interface SSHKey {
  id: string;
  name: string;
  publicKey: string;
  fingerprint: string;
  createdAt: string;
}

export interface KubeEvent {
  type: string;
  reason: string;
  message: string;
  object: string;
  count: number;
  time: string;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    credentials: "same-origin",
    headers: {
      "X-Requested-With": "devenv",
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    let msg = res.statusText;
    try {
      const data = await res.json();
      if (data?.error) msg = data.error;
    } catch {
      /* ignore */
    }
    throw new ApiError(res.status, msg);
  }
  if (res.status === 204 || res.status === 202) return undefined as T;
  return res.json() as Promise<T>;
}

const enc = encodeURIComponent;

export const api = {
  info: () => request<Info>("GET", "/info"),
  me: () => request<User>("GET", "/me"),
  login: (username: string, password: string) => request<User>("POST", "/login", { username, password }),
  logout: () => request<void>("POST", "/logout"),

  images: () => request<Image[]>("GET", "/images"),
  createImage: (img: ImageInput) => request<Image>("POST", "/images", img),
  updateImage: (name: string, img: ImageInput) => request<Image>("PUT", `/images/${enc(name)}`, img),
  deleteImage: (name: string) => request<void>("DELETE", `/images/${enc(name)}`),

  classes: () => request<Class[]>("GET", "/classes"),
  capacity: (cls: string, exclude?: string) =>
    request<Capacity>("GET", `/classes/${enc(cls)}/capacity${exclude ? `?exclude=${enc(exclude)}` : ""}`),

  environments: () => request<Environment[]>("GET", "/environments"),
  environment: (name: string) => request<Environment>("GET", `/environments/${enc(name)}`),
  createEnvironment: (e: CreateEnvironment) => request<Environment>("POST", "/environments", e),
  updateEnvironment: (name: string, e: UpdateEnvironment) =>
    request<Environment>("PATCH", `/environments/${enc(name)}`, e),
  deleteEnvironment: (name: string) => request<void>("DELETE", `/environments/${enc(name)}`),
  startEnvironment: (name: string) => request<Environment>("POST", `/environments/${enc(name)}/start`),
  stopEnvironment: (name: string) => request<Environment>("POST", `/environments/${enc(name)}/stop`),
  events: (name: string) => request<KubeEvent[]>("GET", `/environments/${enc(name)}/events`),

  sshKeys: () => request<SSHKey[]>("GET", "/sshkeys"),
  createSSHKey: (name: string, publicKey: string) => request<SSHKey>("POST", "/sshkeys", { name, publicKey }),
  deleteSSHKey: (id: string) => request<void>("DELETE", `/sshkeys/${enc(id)}`),
};
