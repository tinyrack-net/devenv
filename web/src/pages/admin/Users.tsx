import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Shield, UserPlus, Users as UsersIcon } from "lucide-react";
import { api, type LimitsInput, type User } from "../../lib/api";
import { formatBytes, formatCPU } from "../../lib/quantity";
import { useMe } from "../../lib/prefs";
import { PageHeader } from "../../components/Layout";
import { Confirm, Empty, ErrorAlert, Field, Loading, Modal, Spinner, cx, timeAgo } from "../../components/ui";
import { useToast } from "../../components/toast";
import { AdminOnly } from "./AdminOnly";

function LimitsForm({ value, onChange }: { value: LimitsInput; onChange: (v: LimitsInput) => void }) {
  const num = (v: string) => (v === "" ? undefined : Number(v));
  return (
    <div className="grid gap-x-3 sm:grid-cols-5">
      <Field label="Environments">
        <input className="input input-sm w-full" type="number" min={0} value={value.maxEnvironments ?? ""} placeholder="default" onChange={(e) => onChange({ ...value, maxEnvironments: num(e.target.value) })} />
      </Field>
      <Field label="Running">
        <input className="input input-sm w-full" type="number" min={0} value={value.maxRunning ?? ""} placeholder="default" onChange={(e) => onChange({ ...value, maxRunning: num(e.target.value) })} />
      </Field>
      <Field label="CPU">
        <input className="input input-sm w-full" value={value.cpu ?? ""} placeholder="default" onChange={(e) => onChange({ ...value, cpu: e.target.value || undefined })} />
      </Field>
      <Field label="Memory">
        <input className="input input-sm w-full" value={value.memory ?? ""} placeholder="default" onChange={(e) => onChange({ ...value, memory: e.target.value || undefined })} />
      </Field>
      <Field label="Disk">
        <input className="input input-sm w-full" value={value.disk ?? ""} placeholder="default" onChange={(e) => onChange({ ...value, disk: e.target.value || undefined })} />
      </Field>
    </div>
  );
}

function EditUser({ user, onDone }: { user: User; onDone: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [role, setRole] = useState(user.role);
  const [disabled, setDisabled] = useState(user.disabled);
  const [displayName, setDisplayName] = useState(user.displayName ?? "");
  const [email, setEmail] = useState(user.email ?? "");
  const [limits, setLimits] = useState<LimitsInput>(user.limits);
  const [ids, setIds] = useState(user.identities);
  const [del, setDel] = useState(false);
  const [purge, setPurge] = useState(false);
  const save = useMutation({
    mutationFn: () => api.admin.updateUser(user.name, { role, disabled, displayName, email, limits, identities: ids }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
      toast("User updated", "success");
      onDone();
    },
  });
  const remove = useMutation({
    mutationFn: () => api.admin.deleteUser(user.name, purge),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
      onDone();
    },
  });
  return (
    <div className="space-y-2">
      <div className="grid gap-x-4 sm:grid-cols-2">
        <Field label="Display name">
          <input className="input w-full" value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
        </Field>
        <Field label="E-mail" hint="Used to link the first OIDC login to this user.">
          <input className="input w-full" value={email} onChange={(e) => setEmail(e.target.value)} />
        </Field>
        <Field label="Role" hint={user.admin && role !== "admin" ? "Still an administrator through configuration or an admin group." : undefined}>
          <select className="select w-full" value={role} onChange={(e) => setRole(e.target.value)} disabled={user.local}>
            <option value="user">User</option>
            <option value="admin">Administrator</option>
          </select>
        </Field>
        <Field label="Status">
          <label className="label gap-2">
            <input type="checkbox" className="toggle toggle-error toggle-sm" checked={disabled} onChange={(e) => setDisabled(e.target.checked)} disabled={user.local} />
            Disabled (sign-in, tokens and SSH keys rejected)
          </label>
        </Field>
      </div>
      <h3 className="pt-2 text-sm font-medium">Limits</h3>
      <LimitsForm value={limits} onChange={setLimits} />
      <h3 className="pt-2 text-sm font-medium">Linked logins</h3>
      {ids.length === 0 && <p className="text-sm text-base-content/60">None.</p>}
      <ul className="space-y-1">
        {ids.map((id) => (
          <li key={id.provider + id.subject} className="flex items-center gap-2 text-sm">
            <span className="badge badge-sm">{id.provider}</span>
            <code className="font-mono-tight flex-1 truncate">{id.subject}</code>
            <button className="btn btn-ghost btn-xs" onClick={() => setIds(ids.filter((x) => x !== id))}>
              Unlink
            </button>
          </li>
        ))}
      </ul>
      <ErrorAlert error={save.error ?? remove.error} />
      <div className="modal-action justify-between">
        {!user.local ? (
          <button className="btn btn-ghost text-error" onClick={() => setDel(true)}>
            Delete user
          </button>
        ) : (
          <span />
        )}
        <div className="flex gap-2">
          <button className="btn btn-ghost" onClick={onDone}>
            Cancel
          </button>
          <button className="btn btn-primary" disabled={save.isPending} onClick={() => save.mutate()}>
            {save.isPending && <Spinner className="loading-xs" />} Save
          </button>
        </div>
      </div>
      <Confirm open={del} onClose={() => setDel(false)} title={`Delete ${user.name}?`} danger confirmLabel="Delete" typeToConfirm={user.name} busy={remove.isPending} onConfirm={() => remove.mutate()}>
        <p>The user and their access tokens are deleted.</p>
        <label className="label gap-2">
          <input type="checkbox" className="checkbox checkbox-sm checkbox-error" checked={purge} onChange={(e) => setPurge(e.target.checked)} />
          Also delete their environments and personal images
        </label>
      </Confirm>
    </div>
  );
}

function CreateUser({ onDone }: { onDone: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [role, setRole] = useState("user");
  const create = useMutation({
    mutationFn: () => api.admin.createUser({ name, email, role }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
      onDone();
    },
  });
  return (
    <div>
      <p className="mb-2 text-sm text-base-content/60">Pre-create a user to set limits before the first sign-in, or when OIDC providers do not create users automatically. The first OIDC login with a verified matching e-mail is linked to this user.</p>
      <Field label="User name">
        <input className="input w-full" value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="E-mail">
        <input className="input w-full" value={email} onChange={(e) => setEmail(e.target.value)} />
      </Field>
      <Field label="Role">
        <select className="select w-full" value={role} onChange={(e) => setRole(e.target.value)}>
          <option value="user">User</option>
          <option value="admin">Administrator</option>
        </select>
      </Field>
      <ErrorAlert error={create.error} className="mt-2" />
      <div className="modal-action">
        <button className="btn btn-ghost" onClick={onDone}>
          Cancel
        </button>
        <button className="btn btn-primary" disabled={!name || create.isPending} onClick={() => create.mutate()}>
          Create
        </button>
      </div>
    </div>
  );
}

export function AdminUsersPage() {
  const me = useMe();
  const users = useQuery({ queryKey: ["admin", "users"], queryFn: api.admin.users, enabled: !!me.data?.admin });
  const [edit, setEdit] = useState<User | null>(null);
  const [creating, setCreating] = useState(false);
  return (
    <AdminOnly>
      <PageHeader
        title="Users"
        actions={
          <button className="btn btn-primary btn-sm" onClick={() => setCreating(true)}>
            <UserPlus className="size-4" /> Add user
          </button>
        }
      />
      <ErrorAlert error={users.error} />
      {users.isLoading && <Loading />}
      {users.data?.length === 0 && <Empty icon={<UsersIcon className="size-10" />} title="No users" />}
      {!!users.data?.length && (
        <div className="overflow-x-auto rounded-box border border-base-300 bg-base-100">
          <table className="table">
            <thead>
              <tr>
                <th>User</th>
                <th>Role</th>
                <th>Usage</th>
                <th>Last sign-in</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {users.data.map((u) => (
                <tr key={u.name} className={cx(u.disabled && "opacity-50")}>
                  <td>
                    <div className="font-medium">
                      {u.displayName || u.name}
                      {u.local && <span className="badge badge-ghost badge-xs ml-2">local</span>}
                      {u.disabled && <span className="badge badge-error badge-xs ml-2">disabled</span>}
                    </div>
                    <div className="text-xs text-base-content/50">
                      {u.name}
                      {u.email && ` · ${u.email}`}
                    </div>
                  </td>
                  <td>
                    {u.admin ? (
                      <span className="badge badge-primary badge-soft badge-sm gap-1">
                        <Shield className="size-3" /> admin
                      </span>
                    ) : (
                      <span className="badge badge-sm">user</span>
                    )}
                    {u.groups.length > 0 && <div className="mt-1 text-xs text-base-content/50">{u.groups.join(", ")}</div>}
                  </td>
                  <td className="text-sm">
                    {u.usage && (
                      <Link to={`/admin/environments?owner=${u.name}`} className="link-hover">
                        {u.usage.environments} envs ({u.usage.running} running) · {formatCPU(u.usage.cpu)} · {formatBytes(u.usage.memory)}
                      </Link>
                    )}
                  </td>
                  <td className="text-sm">
                    {timeAgo(u.lastLoginAt)}
                    {u.lastLoginProvider && <div className="text-xs text-base-content/50">{u.lastLoginProvider}</div>}
                  </td>
                  <td className="text-right">
                    <button className="btn btn-ghost btn-sm" onClick={() => setEdit(u)}>
                      Edit
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Modal open={!!edit} onClose={() => setEdit(null)} title={`Edit ${edit?.name}`} wide>
        {edit && <EditUser user={edit} onDone={() => setEdit(null)} />}
      </Modal>
      <Modal open={creating} onClose={() => setCreating(false)} title="Add user">
        <CreateUser onDone={() => setCreating(false)} />
      </Modal>
    </AdminOnly>
  );
}
