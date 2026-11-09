// Minimal Kubernetes quantity helpers.

const binary: Record<string, number> = { Ki: 2 ** 10, Mi: 2 ** 20, Gi: 2 ** 30, Ti: 2 ** 40, Pi: 2 ** 50 };
const decimal: Record<string, number> = { n: 1e-9, u: 1e-6, m: 1e-3, "": 1, k: 1e3, M: 1e6, G: 1e9, T: 1e12, P: 1e15 };

/** Parses a quantity into a base-unit number (cores or bytes). */
export function parseQuantity(q: string | undefined | null): number | undefined {
  if (!q) return undefined;
  const m = /^([0-9.]+)([a-zA-Z]*)$/.exec(q.trim());
  if (!m) return undefined;
  const n = Number(m[1]);
  const unit = m[2];
  if (unit in binary) return n * binary[unit];
  if (unit in decimal) return n * decimal[unit];
  return undefined;
}

/** CPU cores, rounded down to 0.5 steps. */
export function cores(q: string | undefined): number | undefined {
  const v = parseQuantity(q);
  return v === undefined ? undefined : Math.floor(v * 2) / 2;
}

/** Bytes expressed in GiB, rounded down. */
export function gib(q: string | undefined): number | undefined {
  const v = parseQuantity(q);
  return v === undefined ? undefined : Math.floor(v / 2 ** 30);
}

/** Bytes expressed in MiB, rounded down. */
export function mib(q: string | undefined): number | undefined {
  const v = parseQuantity(q);
  return v === undefined ? undefined : Math.floor(v / 2 ** 20);
}

export function formatCPU(q: string | undefined): string {
  const v = parseQuantity(q);
  if (v === undefined) return "-";
  return `${Number(v.toFixed(2))} CPU`;
}

export function formatBytes(q: string | number | undefined): string {
  const v = typeof q === "number" ? q : parseQuantity(q);
  if (v === undefined) return "-";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  let n = v;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${Number(n.toFixed(1))} ${units[i]}`;
}

/** Fraction used/limit in [0,1], or undefined without a limit. */
export function ratio(used: string | undefined, limit: string | undefined): number | undefined {
  const u = parseQuantity(used) ?? 0;
  const l = parseQuantity(limit);
  if (!l) return undefined;
  return Math.min(1, u / l);
}

/** CPU and memory of an environment for display ("2 CPU · 4 GiB"). Uses the
 * effective values (defaults applied) and falls back to the request. */
export function envSize(env: { resources: { cpu?: string; memory?: string }; effective?: { cpu?: string; memory?: string } }): { cpu: string; memory: string } {
  const cpu = env.effective?.cpu || env.resources.cpu;
  const mem = env.effective?.memory || env.resources.memory;
  return { cpu: cpu ? `${Number((parseQuantity(cpu) ?? 0).toFixed(2))}` : "-", memory: mem ? formatBytes(mem) : "-" };
}
