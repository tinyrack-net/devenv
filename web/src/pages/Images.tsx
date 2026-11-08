import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Layers, Lock, Monitor, Pencil, Plus, SquareTerminal, Trash2, Users } from "lucide-react";
import { api, type Image, type ImageInput } from "../lib/api";
import { useMe } from "../lib/prefs";
import { PageHeader } from "../components/Layout";
import { Confirm, Empty, ErrorAlert, Field, Loading, Modal, Spinner, TagInput, cx } from "../components/ui";
import { useToast } from "../components/toast";

function blank(): ImageInput {
  return { name: "", image: "", desktop: false, disabled: false, defaultResources: {}, access: { users: [], groups: [] } };
}

function ImageForm({ initial, editing, onDone }: { initial: ImageInput; editing?: Image; onDone: () => void }) {
  const qc = useQueryClient();
  const me = useMe();
  const toast = useToast();
  const [v, setV] = useState<ImageInput>(initial);
  const admin = !!me.data?.admin;
  const sharedImage = editing ? !editing.owner : !!v.shared;
  const save = useMutation({
    mutationFn: () => (editing ? api.updateImage(editing.name, v) : api.createImage(v)),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["images"] });
      toast(editing ? "Image updated" : "Image added", "success");
      onDone();
    },
  });
  const set = (patch: Partial<ImageInput>) => setV((x) => ({ ...x, ...patch }));
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
      className="space-y-1"
    >
      <div className="grid gap-x-4 sm:grid-cols-2">
        <Field label="Name" hint="Used as the image id.">
          <input className="input w-full" value={v.name} disabled={!!editing} onChange={(e) => set({ name: e.target.value })} required={!editing} />
        </Field>
        <Field label="Display name">
          <input className="input w-full" value={v.displayName ?? ""} onChange={(e) => set({ displayName: e.target.value })} />
        </Field>
      </div>
      <Field label="Container image" hint="Needs sshd and sftp-server; desktop images also run VNC on 127.0.0.1:5901. See docs/images.md.">
        <input className="input font-mono-tight w-full" value={v.image} onChange={(e) => set({ image: e.target.value })} placeholder="ghcr.io/org/image:tag" required />
      </Field>
      <Field label="Description">
        <input className="input w-full" value={v.description ?? ""} onChange={(e) => set({ description: e.target.value })} />
      </Field>
      <div className="grid gap-x-4 sm:grid-cols-3">
        <Field label="Default CPU">
          <input className="input w-full" value={v.defaultResources.cpu ?? ""} onChange={(e) => set({ defaultResources: { ...v.defaultResources, cpu: e.target.value || undefined } })} placeholder="1" />
        </Field>
        <Field label="Default memory">
          <input className="input w-full" value={v.defaultResources.memory ?? ""} onChange={(e) => set({ defaultResources: { ...v.defaultResources, memory: e.target.value || undefined } })} placeholder="2Gi" />
        </Field>
        <Field label="Default disk">
          <input className="input w-full" value={v.defaultResources.disk ?? ""} onChange={(e) => set({ defaultResources: { ...v.defaultResources, disk: e.target.value || undefined } })} placeholder="10Gi" />
        </Field>
      </div>
      <div className="flex flex-wrap gap-x-6 gap-y-2 pt-3">
        <label className="label gap-2">
          <input type="checkbox" className="checkbox checkbox-sm" checked={v.desktop} onChange={(e) => set({ desktop: e.target.checked })} /> Provides a desktop (VNC)
        </label>
        <label className="label gap-2">
          <input type="checkbox" className="checkbox checkbox-sm" checked={v.disabled} onChange={(e) => set({ disabled: e.target.checked })} /> Disabled for new environments
        </label>
        {admin && (
          <label className="label gap-2">
            <input type="checkbox" className="checkbox checkbox-sm" checked={!!v.prepull} onChange={(e) => set({ prepull: e.target.checked })} /> Prepull on every node
          </label>
        )}
        {admin && !editing && (
          <label className="label gap-2">
            <input type="checkbox" className="checkbox checkbox-sm" checked={!!v.shared} onChange={(e) => set({ shared: e.target.checked })} /> Shared with everyone
          </label>
        )}
      </div>
      {admin && sharedImage && (
        <div className="grid gap-x-4 pt-2 sm:grid-cols-2">
          <Field label="Restrict to groups" hint="Empty: everyone can use it.">
            <TagInput value={v.access?.groups ?? []} onChange={(g) => set({ access: { users: v.access?.users ?? [], groups: g } })} placeholder="group" />
          </Field>
          <Field label="Restrict to users">
            <TagInput value={v.access?.users ?? []} onChange={(u) => set({ access: { groups: v.access?.groups ?? [], users: u } })} placeholder="user" />
          </Field>
        </div>
      )}
      <ErrorAlert error={save.error} className="mt-3" />
      <div className="modal-action">
        <button type="button" className="btn btn-ghost" onClick={onDone}>
          Cancel
        </button>
        <button className="btn btn-primary" disabled={save.isPending}>
          {save.isPending && <Spinner className="loading-xs" />} Save
        </button>
      </div>
    </form>
  );
}

function ImageRow({ img, onEdit }: { img: Image; onEdit: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [confirm, setConfirm] = useState(false);
  const del = useMutation({
    mutationFn: () => api.deleteImage(img.name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["images"] });
      setConfirm(false);
    },
    onError: (e) => toast(String((e as Error).message), "error"),
  });
  const restricted = img.access.groups.length + img.access.users.length > 0;
  return (
    <tr className={cx(img.disabled && "opacity-60")}>
      <td>
        <div className="flex items-center gap-2 font-medium">
          {img.desktop ? <Monitor className="size-4" /> : <SquareTerminal className="size-4" />}
          {img.displayName || img.name}
        </div>
        <div className="text-xs text-base-content/50">{img.description}</div>
      </td>
      <td data-label="Image">
        <code className="font-mono-tight break-all">{img.image}</code>
      </td>
      <td data-label="Scope">
        <div className="flex flex-wrap gap-1">
          {img.owner ? (
            <span className="badge badge-soft badge-sm gap-1">
              <Lock className="size-3" /> {img.owner}
            </span>
          ) : (
            <span className="badge badge-soft badge-primary badge-sm gap-1">
              <Users className="size-3" /> {restricted ? "restricted" : "shared"}
            </span>
          )}
          {img.prepull && <span className="badge badge-soft badge-sm">prepulled</span>}
          {img.disabled && <span className="badge badge-soft badge-warning badge-sm">disabled</span>}
        </div>
      </td>
      <td data-label="In use" className="text-sm">{img.inUse}</td>
      <td>
        {img.canManage && (
          <div className="flex justify-end gap-1">
            <button className="btn btn-ghost btn-xs" onClick={onEdit}>
              <Pencil className="size-3.5" />
            </button>
            <button className="btn btn-ghost btn-xs text-error" onClick={() => setConfirm(true)} disabled={img.inUse > 0} title={img.inUse > 0 ? "In use; disable it instead" : "Delete"}>
              <Trash2 className="size-3.5" />
            </button>
          </div>
        )}
        <Confirm open={confirm} onClose={() => setConfirm(false)} title={`Delete image ${img.name}?`} danger confirmLabel="Delete" busy={del.isPending} onConfirm={() => del.mutate()} />
      </td>
    </tr>
  );
}

export function ImagesPage() {
  const me = useMe();
  const [all, setAll] = useState(false);
  const images = useQuery({ queryKey: ["images", all], queryFn: () => api.images(all) });
  const [editing, setEditing] = useState<Image | null>(null);
  const [creating, setCreating] = useState(false);
  return (
    <>
      <PageHeader
        title="Images"
        subtitle="Shared images are managed by administrators. Add your own images for personal use."
        actions={
          <>
            {me.data?.admin && (
              <label className="label gap-2 text-sm">
                <input type="checkbox" className="toggle toggle-sm" checked={all} onChange={(e) => setAll(e.target.checked)} /> Show users' images
              </label>
            )}
            <button className="btn btn-primary btn-sm" onClick={() => setCreating(true)}>
              <Plus className="size-4" /> Add image
            </button>
          </>
        }
      />
      <ErrorAlert error={images.error} />
      {images.isLoading && <Loading />}
      {images.data?.length === 0 && <Empty icon={<Layers className="size-10" />} title="No images" />}
      {!!images.data?.length && (
        <div className="overflow-x-auto rounded-box border border-base-300 bg-base-100">
          <table className="table table-stack">
            <thead>
              <tr>
                <th>Name</th>
                <th>Image</th>
                <th>Scope</th>
                <th>In use</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {images.data.map((i) => (
                <ImageRow key={i.name} img={i} onEdit={() => setEditing(i)} />
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Modal open={creating} onClose={() => setCreating(false)} title="Add image" wide>
        <ImageForm initial={blank()} onDone={() => setCreating(false)} />
      </Modal>
      <Modal open={!!editing} onClose={() => setEditing(null)} title={`Edit ${editing?.name}`} wide>
        {editing && (
          <ImageForm
            editing={editing}
            initial={{
              image: editing.image,
              displayName: editing.displayName,
              description: editing.description,
              desktop: editing.desktop,
              disabled: editing.disabled,
              prepull: editing.prepull,
              imagePullPolicy: editing.imagePullPolicy,
              defaultResources: editing.defaultResources,
              access: editing.access,
            }}
            onDone={() => setEditing(null)}
          />
        )}
      </Modal>
    </>
  );
}
