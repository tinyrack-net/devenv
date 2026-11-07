import { useEffect, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft } from "lucide-react";
import { api, ApiError, type AccessInput, type Environment, type PortInput, type ResourcesInput, type UpdateEnvironmentRequest } from "../lib/api";
import { gib } from "../lib/quantity";
import { useMe } from "../lib/prefs";
import { PageHeader } from "../components/Layout";
import { PortsInput, ResourceInputs, SharingInput } from "../components/EnvForm";
import { Confirm, ErrorAlert, Field, Loading, PhaseBadge, Spinner } from "../components/ui";
import { useToast } from "../components/toast";
import { useEnvMutations } from "../components/EnvActions";

function Section({ title, description, children }: { title: string; description?: string; children: ReactNode }) {
  return (
    <section className="grid gap-4 rounded-box bg-base-100 p-5 md:grid-cols-[14rem_1fr]">
      <div>
        <h2 className="font-semibold">{title}</h2>
        {description && <p className="mt-1 text-sm text-base-content/60">{description}</p>}
      </div>
      <div>{children}</div>
    </section>
  );
}

export function EnvironmentSettingsPage() {
  const { name = "" } = useParams();
  const env = useQuery({ queryKey: ["environment", name], queryFn: () => api.environment(name) });
  if (env.error instanceof ApiError && env.error.status === 404) return <ErrorAlert error={new Error(`Environment ${name} not found`)} />;
  if (!env.data) return env.error ? <ErrorAlert error={env.error} /> : <Loading />;
  return <Settings env={env.data} />;
}

function useUpdate(env: Environment) {
  const qc = useQueryClient();
  const toast = useToast();
  return useMutation({
    mutationFn: (r: UpdateEnvironmentRequest) => api.updateEnvironment(env.name, r),
    onSuccess: (e) => {
      qc.setQueryData(["environment", e.name], e);
      qc.invalidateQueries({ queryKey: ["environments"] });
      toast("Saved", "success");
    },
  });
}

function Settings({ env }: { env: Environment }) {
  const navigate = useNavigate();
  const me = useMe();
  const manage = env.access === "manage";
  const images = useQuery({ queryKey: ["images"], queryFn: () => api.images() });
  const classes = useQuery({ queryKey: ["classes"], queryFn: api.classes });
  const users = useQuery({ queryKey: ["users"], queryFn: api.users, enabled: !!me.data?.admin });
  const klass = classes.data?.find((c) => c.name === env.class);
  const img = images.data?.find((i) => i.name === env.image);
  const m = useEnvMutations(env);

  const [res, setRes] = useState<ResourcesInput>(env.resources);
  const [image, setImage] = useState(env.image);
  const [ports, setPorts] = useState<PortInput[]>(env.ports);
  const [sharing, setSharing] = useState<AccessInput>(env.sharing);
  const [keys, setKeys] = useState(env.sshAuthorizedKeys.join("\n"));
  const [password, setPassword] = useState("");
  const [owner, setOwner] = useState(env.owner ?? "");
  const [confirmDelete, setConfirmDelete] = useState(false);
  useEffect(() => {
    setRes(env.resources);
    setImage(env.image);
    setPorts(env.ports);
    setSharing(env.sharing);
    setKeys(env.sshAuthorizedKeys.join("\n"));
    setOwner(env.owner ?? "");
  }, [env.name]); // eslint-disable-line react-hooks/exhaustive-deps

  const spec = useUpdate(env);
  const share = useUpdate(env);
  const net = useUpdate(env);
  const access = useUpdate(env);
  const transfer = useUpdate(env);

  return (
    <>
      <Link to={`/env/${env.name}`} className="btn btn-ghost btn-sm mb-2 -ml-2">
        <ArrowLeft className="size-4" /> Back to workspace
      </Link>
      <PageHeader title={<span className="flex items-center gap-3">{env.name} <PhaseBadge phase={env.phase} deleting={env.deleting} size="md" /></span>} subtitle={`Owned by ${env.owner}${manage ? "" : " · shared with you (read only)"}`} />
      <div className="space-y-4">
        <Section title="Image and resources" description="Changing the image or CPU/memory restarts the environment. Files in /home/dev are kept.">
          <fieldset disabled={!manage} className="space-y-3">
            <Field label="Image">
              <select className="select w-full max-w-md" value={image} onChange={(e) => setImage(e.target.value)}>
                {images.data
                  ?.filter((i) => !i.disabled || i.name === env.image)
                  .map((i) => (
                    <option key={i.name} value={i.name}>
                      {i.displayName || i.name} ({i.image})
                    </option>
                  ))}
              </select>
            </Field>
            {klass && (
              <ResourceInputs
                className={env.class}
                value={res}
                defaults={{ ...(img?.defaultResources ?? {}), disk: img?.defaultResources.disk ?? klass.defaultDisk }}
                onChange={setRes}
                exclude={env.name}
                minDisk={gib(env.resources.disk)}
                persistent={klass.storageMode === "persistent"}
              />
            )}
            <ErrorAlert error={spec.error} />
            {manage && (
              <button
                className="btn btn-primary btn-sm"
                disabled={spec.isPending}
                onClick={() => spec.mutate({ image: image !== env.image ? image : undefined, resources: res })}
              >
                {spec.isPending && <Spinner className="loading-xs" />} Save and apply
              </button>
            )}
          </fieldset>
        </Section>

        <Section title="Sharing" description="People you share with can use the environment: terminals, desktop, SSH, start and stop. Only you and administrators can change or delete it.">
          <fieldset disabled={!manage} className="space-y-3">
            <SharingInput value={sharing} onChange={setSharing} />
            <ErrorAlert error={share.error} />
            {manage && (
              <button className="btn btn-primary btn-sm" disabled={share.isPending} onClick={() => share.mutate({ sharing })}>
                Save sharing
              </button>
            )}
          </fieldset>
        </Section>

        <Section title="Access" description="Your SSH keys and the keys of users you share with always work. Extra keys are for CI or deploy keys.">
          <fieldset disabled={!manage} className="space-y-3">
            <Field label="Extra SSH public keys (one per line)">
              <textarea className="textarea font-mono-tight w-full" rows={3} value={keys} onChange={(e) => setKeys(e.target.value)} />
            </Field>
            <Field label={`Password for user dev ${env.passwordSet ? "(set)" : "(not set)"}`} hint="Enables SSH password login. Leave empty to keep it unchanged.">
              <input className="input w-full max-w-md" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
            <ErrorAlert error={access.error} />
            {manage && (
              <div className="flex gap-2">
                <button
                  className="btn btn-primary btn-sm"
                  disabled={access.isPending}
                  onClick={() =>
                    access.mutate(
                      { sshAuthorizedKeys: keys.split("\n").filter((l) => l.trim()), password: password || undefined },
                      { onSuccess: () => setPassword("") },
                    )
                  }
                >
                  Save access
                </button>
                {env.passwordSet && (
                  <button className="btn btn-ghost btn-sm" onClick={() => access.mutate({ password: "" })}>
                    Remove password
                  </button>
                )}
              </div>
            )}
          </fieldset>
        </Section>

        {env.networkMode === "loadBalancer" && (
          <Section title="Ports" description={`Exposed on ${env.ip ?? "the environment address"} besides SSH.`}>
            <fieldset disabled={!manage} className="space-y-3">
              <PortsInput value={ports} onChange={setPorts} />
              <ErrorAlert error={net.error} />
              {manage && (
                <button className="btn btn-primary btn-sm" onClick={() => net.mutate({ ports: ports.filter((p) => p.port > 0) })}>
                  Save ports
                </button>
              )}
            </fieldset>
          </Section>
        )}

        {me.data?.admin && (
          <Section title="Owner" description="Transfer the environment to another user.">
            <div className="flex items-end gap-2">
              <select className="select w-full max-w-xs" value={owner} onChange={(e) => setOwner(e.target.value)}>
                {users.data?.map((u) => (
                  <option key={u.name} value={u.name}>
                    {u.name}
                  </option>
                ))}
              </select>
              <button className="btn btn-sm" disabled={owner === env.owner || transfer.isPending} onClick={() => transfer.mutate({ owner })}>
                Transfer
              </button>
            </div>
            <ErrorAlert error={transfer.error} className="mt-2" />
          </Section>
        )}

        <Section title="Details">
          <dl className="grid grid-cols-[8rem_1fr] gap-y-1 text-sm">
            <dt className="text-base-content/60">Class</dt>
            <dd>{env.class}</dd>
            <dt className="text-base-content/60">Network</dt>
            <dd>{env.networkMode ?? "-"}</dd>
            <dt className="text-base-content/60">Address</dt>
            <dd className="font-mono-tight">{env.ip ?? "-"} {env.mac && `(${env.mac})`}</dd>
            <dt className="text-base-content/60">Node</dt>
            <dd>{env.nodeName ?? "-"}</dd>
            <dt className="text-base-content/60">Image</dt>
            <dd className="font-mono-tight break-all">{env.imageRef ?? "-"}</dd>
            <dt className="text-base-content/60">Created</dt>
            <dd>{new Date(env.createdAt).toLocaleString()}</dd>
          </dl>
        </Section>

        {manage && (
          <section className="rounded-box border border-error/40 bg-base-100 p-5">
            <h2 className="font-semibold text-error">Delete environment</h2>
            <p className="mt-1 mb-3 text-sm text-base-content/60">The container and its home volume are deleted permanently.</p>
            <button className="btn btn-error btn-sm" onClick={() => setConfirmDelete(true)}>
              Delete {env.name}
            </button>
            <Confirm
              open={confirmDelete}
              onClose={() => setConfirmDelete(false)}
              title={`Delete ${env.name}?`}
              danger
              confirmLabel="Delete"
              typeToConfirm={env.name}
              busy={m.remove.isPending}
              onConfirm={() => m.remove.mutate(undefined, { onSuccess: () => navigate("/") })}
            />
          </section>
        )}
      </div>
    </>
  );
}
