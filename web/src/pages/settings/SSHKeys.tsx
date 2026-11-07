import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Trash2 } from "lucide-react";
import { api } from "../../lib/api";
import { useInfo } from "../../lib/prefs";
import { PageHeader } from "../../components/Layout";
import { CopyField, Empty, ErrorAlert, Field, Loading, Spinner, timeAgo } from "../../components/ui";

export function SSHKeysPage() {
  const qc = useQueryClient();
  const info = useInfo();
  const keys = useQuery({ queryKey: ["sshkeys"], queryFn: api.sshKeys });
  const [name, setName] = useState("");
  const [key, setKey] = useState("");
  const add = useMutation({
    mutationFn: () => api.addSSHKey(name, key),
    onSuccess: () => {
      setName("");
      setKey("");
      qc.invalidateQueries({ queryKey: ["sshkeys"] });
    },
  });
  const del = useMutation({
    mutationFn: (id: string) => api.deleteSSHKey(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sshkeys"] }),
  });
  return (
    <>
      <PageHeader title="SSH keys" subtitle="Your keys work for every environment you own or that is shared with you, through the gateway and on dedicated addresses." />
      <div className="grid gap-6 lg:grid-cols-[1fr_22rem]">
        <div>
          <ErrorAlert error={keys.error} />
          {keys.isLoading && <Loading />}
          {keys.data?.length === 0 && (
            <Empty icon={<KeyRound className="size-10" />} title="No SSH keys">
              Add a public key to connect with ssh, scp, rsync or editors.
            </Empty>
          )}
          <ul className="space-y-2">
            {keys.data?.map((k) => (
              <li key={k.id} className="flex items-center gap-3 rounded-box bg-base-100 p-4">
                <KeyRound className="size-5 shrink-0 text-base-content/50" />
                <div className="min-w-0 flex-1">
                  <div className="font-medium">{k.name}</div>
                  <code className="font-mono-tight block truncate text-base-content/60">{k.fingerprint}</code>
                  <div className="text-xs text-base-content/50">added {timeAgo(k.createdAt)}</div>
                </div>
                <button className="btn btn-ghost btn-sm text-error" onClick={() => del.mutate(k.id)} disabled={del.isPending}>
                  <Trash2 className="size-4" />
                </button>
              </li>
            ))}
          </ul>
        </div>
        <div className="space-y-4">
          <form
            className="rounded-box bg-base-100 p-5"
            onSubmit={(e) => {
              e.preventDefault();
              add.mutate();
            }}
          >
            <h2 className="font-semibold">Add a key</h2>
            <Field label="Name">
              <input className="input w-full" value={name} onChange={(e) => setName(e.target.value)} placeholder="laptop" />
            </Field>
            <Field label="Public key" hint="Contents of ~/.ssh/id_ed25519.pub or similar.">
              <textarea className="textarea font-mono-tight w-full" rows={4} value={key} onChange={(e) => setKey(e.target.value)} placeholder="ssh-ed25519 AAAA…" required />
            </Field>
            <ErrorAlert error={add.error} className="mt-2" />
            <button className="btn btn-primary mt-3 w-full" disabled={!key || add.isPending}>
              {add.isPending && <Spinner className="loading-xs" />} Add key
            </button>
          </form>
          {info.data?.ssh.hostKey && (
            <div className="rounded-box bg-base-100 p-5 text-sm">
              <h2 className="mb-2 font-semibold">Gateway host key</h2>
              <p className="mb-2 text-base-content/60">Verify the fingerprint on first connect:</p>
              <CopyField value={info.data.ssh.fingerprint ?? ""} />
            </div>
          )}
        </div>
      </div>
    </>
  );
}
