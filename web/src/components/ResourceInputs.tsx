import { useQuery } from "@tanstack/react-query";
import { api, type Resources } from "../api";
import { cores, formatBytes, formatCPU, gib, mib } from "../quantity";
import { Field } from "./common";

interface Props {
  className: string;
  value: Resources;
  defaults: Resources;
  onChange: (r: Resources) => void;
  /** Existing environment name; its own usage is treated as free. */
  exclude?: string;
  /** Minimum disk (GiB), e.g. current size when resizing. */
  minDisk?: number;
  persistent: boolean;
}

/**
 * CPU / memory / disk inputs bounded by the class limits and current cluster capacity.
 */
export function ResourceInputs({ className, value, defaults, onChange, exclude, minDisk, persistent }: Props) {
  const cap = useQuery({
    queryKey: ["capacity", className, exclude],
    queryFn: () => api.capacity(className, exclude),
    enabled: !!className,
    refetchInterval: 15000,
  });
  const maxCPU = cores(cap.data?.maxCPU) ?? 64;
  const maxMem = mib(cap.data?.maxMemory) ?? 256 * 1024;
  const maxDisk = gib(cap.data?.maxDisk) ?? 1024;

  const cpu = cores(value.cpu ?? defaults.cpu) ?? 1;
  const mem = mib(value.memory ?? defaults.memory) ?? 2048;
  const disk = gib(value.disk ?? defaults.disk) ?? 10;

  return (
    <div className="resources">
      <Field
        label={`CPU: ${cpu} core${cpu === 1 ? "" : "s"}`}
        hint={cap.data ? `Available up to ${formatCPU(cap.data.maxCPU)}` : "Checking capacity…"}
      >
        <input
          type="range"
          min={0.5}
          max={Math.max(maxCPU, 0.5)}
          step={0.5}
          value={Math.min(cpu, Math.max(maxCPU, 0.5))}
          onChange={(e) => onChange({ ...value, cpu: e.target.value })}
        />
      </Field>
      <Field
        label={`Memory: ${formatBytes(`${mem}Mi`)}`}
        hint={cap.data ? `Available up to ${formatBytes(cap.data.maxMemory)}` : undefined}
      >
        <input
          type="range"
          min={256}
          max={Math.max(maxMem, 256)}
          step={256}
          value={Math.min(mem, Math.max(maxMem, 256))}
          onChange={(e) => onChange({ ...value, memory: `${e.target.value}Mi` })}
        />
      </Field>
      {persistent && (
        <Field
          label={`Disk: ${disk} GiB`}
          hint={
            <>
              Home directory volume{cap.data?.maxDisk ? `, up to ${formatBytes(cap.data.maxDisk)}` : ""}
              {minDisk ? ". Disks can only grow." : ""}
            </>
          }
        >
          <input
            type="number"
            min={minDisk ?? 1}
            max={maxDisk}
            value={disk}
            onChange={(e) => onChange({ ...value, disk: `${e.target.value}Gi` })}
          />
        </Field>
      )}
      {cap.data && cap.data.nodes.length === 0 && (
        <div className="alert">No schedulable node matches this class.</div>
      )}
    </div>
  );
}
