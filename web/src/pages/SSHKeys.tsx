import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { ErrorBox, Field, timeAgo } from "../components/common";

export function SSHKeysPage() {
  const qc = useQueryClient();
  const keys = useQuery({ queryKey: ["sshkeys"], queryFn: api.sshKeys });
  const [name, setName] = useState("");
  const [publicKey, setPublicKey] = useState("");

  const add = useMutation({
    mutationFn: () => api.createSSHKey(name, publicKey),
    onSuccess: () => {
      setName("");
      setPublicKey("");
      qc.invalidateQueries({ queryKey: ["sshkeys"] });
    },
  });
  const del = useMutation({
    mutationFn: api.deleteSSHKey,
    onSettled: () => qc.invalidateQueries({ queryKey: ["sshkeys"] }),
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    add.mutate();
  };

  return (
    <>
      <div className="page-header">
        <h1>SSH keys</h1>
      </div>
      <p className="muted">Stored keys are offered when creating environments. Existing environments keep their own key list.</p>
      <ErrorBox error={keys.error ?? del.error} />
      <table className="table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Fingerprint</th>
            <th>Added</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {keys.data?.map((k) => (
            <tr key={k.id}>
              <td>{k.name}</td>
              <td className="mono small">{k.fingerprint}</td>
              <td className="small muted">{timeAgo(k.createdAt)}</td>
              <td className="actions">
                <button className="small danger" onClick={() => confirm(`Delete key ${k.name}?`) && del.mutate(k.id)}>
                  Delete
                </button>
              </td>
            </tr>
          ))}
          {keys.data?.length === 0 && (
            <tr>
              <td colSpan={4} className="muted">
                No keys yet.
              </td>
            </tr>
          )}
        </tbody>
      </table>

      <form className="card form" onSubmit={submit}>
        <h2>Add key</h2>
        <ErrorBox error={add.error} />
        <Field label="Name" hint="Optional; defaults to the key comment">
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="laptop" />
        </Field>
        <Field label="Public key">
          <textarea
            className="mono"
            rows={3}
            value={publicKey}
            onChange={(e) => setPublicKey(e.target.value)}
            placeholder="ssh-ed25519 AAAA… user@host"
            required
          />
        </Field>
        <div className="form-actions">
          <button className="primary" disabled={!publicKey || add.isPending}>
            Add
          </button>
        </div>
      </form>
    </>
  );
}
