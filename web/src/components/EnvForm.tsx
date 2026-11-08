import { useQuery } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { api, type AccessInput, type PortInput, type ResourcesInput } from "../lib/api";
import { cores, formatBytes, formatCPU, gib, mib } from "../lib/quantity";
import { Field, TagInput } from "./ui";

/** CPU / memory / disk inputs bounded by class limits and cluster capacity. */
export function ResourceInputs({
  className,
  value,
  defaults,
  onChange,
  exclude,
  minDisk,
  persistent,
}: {
  className: string;
  value: ResourcesInput;
  defaults: ResourcesInput;
  onChange: (r: ResourcesInput) => void;
  exclude?: string;
  minDisk?: number;
  persistent: boolean;
}) {
  const cap = useQuery({
    queryKey: ["capacity", className, exclude],
    queryFn: () => api.capacity(className, exclude),
    enabled: !!className,
    refetchInterval: 15000,
  });
  const maxCPU = Math.max(cores(cap.data?.maxCPU) ?? 64, 0.5);
  const maxMem = Math.max(mib(cap.data?.maxMemory) ?? 256 * 1024, 256);
  const maxDisk = gib(cap.data?.maxDisk) ?? 2048;
  const cpu = cores(value.cpu ?? defaults.cpu) ?? 1;
  const mem = mib(value.memory ?? defaults.memory) ?? 2048;
  const disk = gib(value.disk ?? defaults.disk) ?? 10;

  return (
    <div className="grid gap-x-6 gap-y-2 md:grid-cols-3">
      <Field label={`CPU · ${cpu}`} hint={cap.data ? `up to ${formatCPU(cap.data.maxCPU)} available` : "checking capacity…"}>
        <input
          type="range"
          className="range range-primary range-sm w-full"
          min={0.5}
          max={maxCPU}
          step={0.5}
          value={Math.min(cpu, maxCPU)}
          onChange={(e) => onChange({ ...value, cpu: e.target.value })}
        />
      </Field>
      <Field label={`Memory · ${formatBytes(`${mem}Mi`)}`} hint={cap.data ? `up to ${formatBytes(cap.data.maxMemory)} available` : undefined}>
        <input
          type="range"
          className="range range-primary range-sm w-full"
          min={256}
          max={maxMem}
          step={256}
          value={Math.min(mem, maxMem)}
          onChange={(e) => onChange({ ...value, memory: `${e.target.value}Mi` })}
        />
      </Field>
      {persistent ? (
        <Field
          label="Disk (GiB)"
          hint={
            <>
              Home volume{cap.data?.maxDisk ? `, up to ${formatBytes(cap.data.maxDisk)}` : ""}
              {minDisk ? ". Disks can only grow." : ""}
            </>
          }
        >
          <input
            type="number"
            className="input input-sm w-full"
            min={minDisk ?? 1}
            max={maxDisk}
            value={disk}
            onChange={(e) => onChange({ ...value, disk: `${e.target.value}Gi` })}
          />
        </Field>
      ) : (
        <Field label="Disk" hint="This class uses ephemeral storage: /home/dev is lost on restart.">
          <span className="text-sm text-base-content/60">ephemeral</span>
        </Field>
      )}
      {cap.data && cap.data.nodes.length === 0 && (
        <div className="alert alert-warning alert-soft md:col-span-3">No schedulable node matches this class.</div>
      )}
    </div>
  );
}

/** Users and groups an environment is shared with. */
export function SharingInput({ value, onChange }: { value: AccessInput; onChange: (v: AccessInput) => void }) {
  const users = useQuery({ queryKey: ["users"], queryFn: api.users, staleTime: 60_000 });
  return (
    <div className="grid gap-x-6 md:grid-cols-2">
      <Field label="Users" hint="They can open terminals and the desktop, connect over SSH and start or stop it.">
        <TagInput
          value={value.users}
          onChange={(u) => onChange({ ...value, users: u })}
          placeholder="user name"
          suggestions={(users.data ?? []).map((u) => ({ value: u.name, label: u.displayName }))}
        />
      </Field>
      <Field label="Groups" hint="Identity provider groups.">
        <TagInput value={value.groups} onChange={(g) => onChange({ ...value, groups: g })} placeholder="group" />
      </Field>
    </div>
  );
}

/** Ports exposed on the environment's own address (loadBalancer classes). */
export function PortsInput({ value, onChange }: { value: PortInput[]; onChange: (p: PortInput[]) => void }) {
  return (
    <div className="space-y-2">
      {value.map((p, i) => (
        <div key={i} className="flex flex-wrap items-center gap-2">
          <input
            type="number"
            className="input input-sm w-24"
            min={1}
            max={65535}
            value={p.port || ""}
            placeholder="port"
            onChange={(e) => onChange(value.map((x, j) => (j === i ? { ...x, port: Number(e.target.value) } : x)))}
          />
          <select
            className="select select-sm w-24"
            value={p.protocol ?? "TCP"}
            onChange={(e) => onChange(value.map((x, j) => (j === i ? { ...x, protocol: e.target.value } : x)))}
          >
            <option>TCP</option>
            <option>UDP</option>
          </select>
          <input
            className="input input-sm min-w-32 flex-1"
            placeholder="name (optional)"
            value={p.name ?? ""}
            onChange={(e) => onChange(value.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))}
          />
          <button type="button" className="btn btn-ghost btn-sm btn-square" onClick={() => onChange(value.filter((_, j) => j !== i))}>
            <Trash2 className="size-4" />
          </button>
        </div>
      ))}
      <button type="button" className="btn btn-ghost btn-sm" onClick={() => onChange([...value, { port: 0, protocol: "TCP" }])}>
        <Plus className="size-4" /> Add port
      </button>
    </div>
  );
}
