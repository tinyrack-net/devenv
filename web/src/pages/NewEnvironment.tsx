import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router";
import { api, type Resources } from "../api";
import { ErrorBox, Field } from "../components/common";
import { ResourceInputs } from "../components/ResourceInputs";

const NAME_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

export function NewEnvironmentPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const images = useQuery({ queryKey: ["images"], queryFn: api.images });
  const classes = useQuery({ queryKey: ["classes"], queryFn: api.classes });
  const keys = useQuery({ queryKey: ["sshkeys"], queryFn: api.sshKeys });

  const [name, setName] = useState("");
  const [image, setImage] = useState("");
  const [cls, setCls] = useState("");
  const [resources, setResources] = useState<Resources>({});
  const [selectedKeys, setSelectedKeys] = useState<Set<string> | null>(null);
  const [password, setPassword] = useState("");
  const [password2, setPassword2] = useState("");

  const available = useMemo(() => images.data?.filter((i) => !i.disabled) ?? [], [images.data]);
  const selectedImage = available.find((i) => i.name === image);
  const selectedClass = classes.data?.find((c) => c.name === cls);

  useEffect(() => {
    if (!image && available.length) setImage(available[0].name);
  }, [available, image]);
  useEffect(() => {
    if (!cls && classes.data?.length) setCls(classes.data[0].name);
  }, [classes.data, cls]);
  useEffect(() => {
    if (selectedKeys === null && keys.data) setSelectedKeys(new Set(keys.data.map((k) => k.id)));
  }, [keys.data, selectedKeys]);

  // Reset resources to the image defaults when the image changes.
  useEffect(() => setResources({}), [image]);

  const defaults: Resources = {
    cpu: selectedImage?.defaultResources.cpu,
    memory: selectedImage?.defaultResources.memory,
    disk: selectedImage?.defaultResources.disk ?? selectedClass?.defaultDisk,
  };

  const create = useMutation({
    mutationFn: () =>
      api.createEnvironment({
        name,
        image,
        class: cls,
        resources: {
          cpu: resources.cpu ?? defaults.cpu,
          memory: resources.memory ?? defaults.memory,
          disk: selectedClass?.storageMode === "ephemeral" ? undefined : (resources.disk ?? defaults.disk),
        },
        sshAuthorizedKeys: keys.data?.filter((k) => selectedKeys?.has(k.id)).map((k) => k.publicKey) ?? [],
        password: password || undefined,
      }),
    onSuccess: async (env) => {
      await qc.invalidateQueries({ queryKey: ["environments"] });
      navigate(`/environments/${env.name}`);
    },
  });

  const passwordError =
    password && password.length < 8
      ? "At least 8 characters"
      : password && password !== password2
        ? "Passwords do not match"
        : null;
  const nameError = name && (!NAME_RE.test(name) || name.length > 40) ? "Lowercase letters, digits and '-', max 40" : null;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate();
  };


  return (
    <>
      <div className="page-header">
        <h1>New environment</h1>
      </div>
      <form className="card form" onSubmit={submit}>
        <ErrorBox error={create.error ?? images.error ?? classes.error} />
        <Field label="Name" hint={nameError ?? "Used as hostname and SSH user."}>
          <input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder="my-project" autoFocus />
        </Field>

        <Field label="Image">
          {available.length === 0 ? (
            <div className="muted">
              No images available. <Link to="/images">Add one</Link>.
            </div>
          ) : (
            <div className="choices">
              {available.map((i) => (
                <button
                  type="button"
                  key={i.name}
                  className={`choice ${image === i.name ? "selected" : ""}`}
                  onClick={() => setImage(i.name)}
                >
                  <strong>{i.displayName || i.name}</strong>
                  {i.desktop && <span className="tag">desktop</span>}
                  <span className="small muted">{i.description || i.image}</span>
                </button>
              ))}
            </div>
          )}
        </Field>

        {(classes.data?.length ?? 0) > 1 && (
          <Field label="Class" hint={selectedClass && `${selectedClass.storageMode} storage · ${selectedClass.networkMode} network`}>
            <select value={cls} onChange={(e) => setCls(e.target.value)}>
              {classes.data!.map((c) => (
                <option key={c.name} value={c.name}>
                  {c.name}
                </option>
              ))}
            </select>
          </Field>
        )}
        {classes.data?.length === 0 && <div className="alert">No DevEnvironmentClass exists. Ask an administrator to create one.</div>}

        {cls && (
          <ResourceInputs
            className={cls}
            value={resources}
            defaults={defaults}
            onChange={setResources}
            persistent={selectedClass?.storageMode !== "ephemeral"}
          />
        )}

        <Field label="SSH keys" hint={<Link to="/ssh-keys">Manage keys</Link>}>
          {keys.data?.length === 0 && <span className="muted">No keys stored. You can still use the web terminal.</span>}
          {keys.data?.map((k) => (
            <label key={k.id} className="checkbox">
              <input
                type="checkbox"
                checked={selectedKeys?.has(k.id) ?? false}
                onChange={(e) => {
                  const next = new Set(selectedKeys);
                  if (e.target.checked) next.add(k.id);
                  else next.delete(k.id);
                  setSelectedKeys(next);
                }}
              />
              {k.name} <span className="muted small mono">{k.fingerprint}</span>
            </label>
          ))}
        </Field>

        <Field
          label="Password (optional)"
          hint={passwordError ?? "Password for the dev user. Enables SSH password login; SSH keys keep working."}
        >
          <div className="row">
            <input
              type="password"
              autoComplete="new-password"
              placeholder="Password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            <input
              type="password"
              autoComplete="new-password"
              placeholder="Confirm"
              value={password2}
              onChange={(e) => setPassword2(e.target.value)}
            />
          </div>
        </Field>

        <div className="form-actions">
          <Link to="/" className="button">
            Cancel
          </Link>
          <button className="primary" disabled={!name || !!nameError || !!passwordError || !image || !cls || create.isPending}>
            {create.isPending ? "Creating…" : "Create"}
          </button>
        </div>
      </form>
    </>
  );
}
