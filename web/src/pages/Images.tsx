import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Image, type ImageInput } from "../api";
import { ErrorBox, Field } from "../components/common";

const empty: ImageInput = {
  name: "",
  image: "",
  displayName: "",
  description: "",
  desktop: false,
  disabled: false,
  imagePullPolicy: "",
  defaultResources: {},
};

export function ImagesPage() {
  const images = useQuery({ queryKey: ["images"], queryFn: api.images });
  const [editing, setEditing] = useState<{ input: ImageInput; isNew: boolean } | null>(null);

  return (
    <>
      <div className="page-header">
        <h1>Images</h1>
        <button className="primary" onClick={() => setEditing({ input: { ...empty }, isNew: true })}>
          Add image
        </button>
      </div>
      <ErrorBox error={images.error} />
      {editing && <ImageForm initial={editing.input} isNew={editing.isNew} onClose={() => setEditing(null)} />}
      <table className="table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Image</th>
            <th>Defaults</th>
            <th>Used by</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {images.data?.map((i) => (
            <Row key={i.name} img={i} onEdit={() => setEditing({ input: toInput(i), isNew: false })} />
          ))}
        </tbody>
      </table>
    </>
  );
}

function toInput(i: Image): ImageInput {
  const { createdAt: _c, inUse: _u, ...rest } = i;
  return { ...rest, defaultResources: { ...i.defaultResources } };
}

function Row({ img, onEdit }: { img: Image; onEdit: () => void }) {
  const qc = useQueryClient();
  const del = useMutation({
    mutationFn: () => api.deleteImage(img.name),
    onSettled: () => qc.invalidateQueries({ queryKey: ["images"] }),
    onError: (e) => alert(e.message),
  });
  const r = img.defaultResources;
  return (
    <tr className={img.disabled ? "disabled" : ""}>
      <td>
        <strong>{img.displayName || img.name}</strong>
        {img.desktop && <span className="tag">desktop</span>}
        {img.disabled && <span className="tag">disabled</span>}
        <div className="small muted">{img.name}</div>
      </td>
      <td className="mono small">{img.image}</td>
      <td className="small">{[r.cpu && `${r.cpu} CPU`, r.memory, r.disk].filter(Boolean).join(" · ") || "-"}</td>
      <td>{img.inUse}</td>
      <td className="actions">
        <button className="small" onClick={onEdit}>
          Edit
        </button>
        <button
          className="small danger"
          disabled={img.inUse > 0 || del.isPending}
          title={img.inUse > 0 ? "In use; disable it instead" : undefined}
          onClick={() => confirm(`Delete image ${img.name}?`) && del.mutate()}
        >
          Delete
        </button>
      </td>
    </tr>
  );
}

function ImageForm({ initial, isNew, onClose }: { initial: ImageInput; isNew: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const [v, setV] = useState<ImageInput>(initial);
  const set = <K extends keyof ImageInput>(k: K, val: ImageInput[K]) => setV({ ...v, [k]: val });
  const setRes = (k: "cpu" | "memory" | "disk", val: string) =>
    setV({ ...v, defaultResources: { ...v.defaultResources, [k]: val || undefined } });

  const save = useMutation({
    mutationFn: () => (isNew ? api.createImage(v) : api.updateImage(v.name, v)),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["images"] });
      onClose();
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate();
  };

  return (
    <form className="card form" onSubmit={submit}>
      <h2>{isNew ? "Add image" : `Edit ${v.name}`}</h2>
      <ErrorBox error={save.error} />
      <div className="row">
        <Field label="Name" hint="Identifier, lowercase">
          <input value={v.name} disabled={!isNew} onChange={(e) => set("name", e.target.value.toLowerCase())} required />
        </Field>
        <Field label="Display name">
          <input value={v.displayName ?? ""} onChange={(e) => set("displayName", e.target.value)} />
        </Field>
      </div>
      <Field label="Container image">
        <input
          className="mono"
          value={v.image}
          onChange={(e) => set("image", e.target.value)}
          placeholder="ghcr.io/tinyrack-net/devenv-base:latest"
          required
        />
      </Field>
      <Field label="Description">
        <input value={v.description ?? ""} onChange={(e) => set("description", e.target.value)} />
      </Field>
      <div className="row">
        <Field label="Default CPU">
          <input value={v.defaultResources.cpu ?? ""} onChange={(e) => setRes("cpu", e.target.value)} placeholder="2" />
        </Field>
        <Field label="Default memory">
          <input value={v.defaultResources.memory ?? ""} onChange={(e) => setRes("memory", e.target.value)} placeholder="4Gi" />
        </Field>
        <Field label="Default disk">
          <input value={v.defaultResources.disk ?? ""} onChange={(e) => setRes("disk", e.target.value)} placeholder="20Gi" />
        </Field>
        <Field label="Pull policy">
          <select value={v.imagePullPolicy ?? ""} onChange={(e) => set("imagePullPolicy", e.target.value)}>
            <option value="">Default</option>
            <option>Always</option>
            <option>IfNotPresent</option>
            <option>Never</option>
          </select>
        </Field>
      </div>
      <label className="checkbox">
        <input type="checkbox" checked={v.desktop} onChange={(e) => set("desktop", e.target.checked)} />
        Provides a VNC desktop (port 5901)
      </label>
      <label className="checkbox">
        <input type="checkbox" checked={v.disabled} onChange={(e) => set("disabled", e.target.checked)} />
        Disabled (hidden for new environments)
      </label>
      <div className="form-actions">
        <button type="button" onClick={onClose}>
          Cancel
        </button>
        <button className="primary" disabled={save.isPending}>
          Save
        </button>
      </div>
    </form>
  );
}
