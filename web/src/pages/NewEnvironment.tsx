import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Check, ChevronDown, Lock, Monitor, Network, SquareTerminal, User } from "lucide-react";
import { api, type AccessInput, type PortInput, type ResourcesInput } from "../lib/api";
import { useMe, usePrefs } from "../lib/prefs";
import { PageHeader } from "../components/Layout";
import { ResourceInputs, PortsInput, SharingInput } from "../components/EnvForm";
import { ErrorAlert, Field, Loading, Spinner, cx } from "../components/ui";
import { UsageSummary } from "./Environments";

const nameRe = /^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$/;

function suggestName(image: string, taken: Set<string>): string {
  const base = image.replace(/[^a-z0-9-]/g, "").slice(0, 30) || "env";
  for (let i = 1; i < 100; i++) {
    const n = `${base}-${i}`;
    if (!taken.has(n)) return n;
  }
  return `${base}-${Date.now() % 10000}`;
}

const networkLabel: Record<string, string> = {
  gateway: "SSH through the gateway",
  loadBalancer: "Own address (LoadBalancer)",
  multus: "Own LAN address",
};

export function NewEnvironmentPage() {
  const navigate = useNavigate();
  const me = useMe();
  const [prefs] = usePrefs();
  const images = useQuery({ queryKey: ["images"], queryFn: () => api.images() });
  const classes = useQuery({ queryKey: ["classes"], queryFn: api.classes });
  const envs = useQuery({ queryKey: ["environments", {}], queryFn: () => api.environments() });
  const users = useQuery({ queryKey: ["users"], queryFn: api.users, enabled: !!me.data?.admin });

  const [image, setImage] = useState("");
  const [cls, setCls] = useState("");
  const [name, setName] = useState("");
  const [nameTouched, setNameTouched] = useState(false);
  const [res, setRes] = useState<ResourcesInput>({});
  const [ports, setPorts] = useState<PortInput[]>([]);
  const [password, setPassword] = useState("");
  const [keys, setKeys] = useState("");
  const [sharing, setSharing] = useState<AccessInput>({ users: [], groups: [] });
  const [owner, setOwner] = useState("");
  const [advanced, setAdvanced] = useState(false);

  const usable = useMemo(() => (images.data ?? []).filter((i) => !i.disabled), [images.data]);
  useEffect(() => {
    if (!image && usable.length) {
      const pref = usable.find((i) => i.name === prefs.defaultImage) ?? usable[0];
      setImage(pref.name);
    }
  }, [usable, image, prefs.defaultImage]);
  useEffect(() => {
    if (!cls && classes.data?.length) {
      setCls((classes.data.find((c) => c.name === prefs.defaultClass) ?? classes.data[0]).name);
    }
  }, [classes.data, cls, prefs.defaultClass]);
  useEffect(() => {
    if (!nameTouched && image && envs.data) setName(suggestName(image, new Set(envs.data.map((e) => e.name))));
  }, [image, envs.data, nameTouched]);

  const img = usable.find((i) => i.name === image);
  const klass = classes.data?.find((c) => c.name === cls);
  useEffect(() => setRes({}), [image]);

  const create = useMutation({
    mutationFn: () =>
      api.createEnvironment({
        name,
        image,
        class: cls,
        resources: res,
        ports: klass?.networkMode === "loadBalancer" ? ports.filter((p) => p.port > 0) : undefined,
        password: password || undefined,
        sshAuthorizedKeys: keys.trim() ? keys.split("\n").filter((l) => l.trim()) : undefined,
        sharing: sharing.users.length || sharing.groups.length ? sharing : undefined,
        owner: owner || undefined,
      }),
    onSuccess: (env) => navigate(`/env/${env.name}`),
  });

  if (images.isLoading || classes.isLoading) return <Loading />;
  const nameOK = nameRe.test(name);
  const shared = usable.filter((i) => !i.owner);
  const personal = usable.filter((i) => i.owner);

  return (
    <>
      <PageHeader title="New environment" subtitle="Pick an image, size it and go." />
      <UsageSummary />
      <form
        className="space-y-6"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <section className="rounded-box bg-base-100 p-4 sm:p-5">
          <h2 className="mb-3 font-semibold">Image</h2>
          {[
            { title: "Shared", list: shared },
            { title: "My images", list: personal },
          ]
            .filter((g) => g.list.length)
            .map((g) => (
              <div key={g.title} className="mb-3">
                {personal.length > 0 && <div className="mb-2 text-xs font-medium text-base-content/50 uppercase">{g.title}</div>}
                <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                  {g.list.map((i) => (
                    <button
                      type="button"
                      key={i.name}
                      onClick={() => setImage(i.name)}
                      className={cx(
                        "relative flex flex-col items-start gap-1 rounded-box border-2 p-4 text-left transition-colors",
                        image === i.name ? "border-primary bg-primary/5" : "border-base-300 hover:border-base-content/30",
                      )}
                    >
                      {image === i.name && <Check className="absolute top-3 right-3 size-4 text-primary" />}
                      <div className="flex items-center gap-2 font-medium">
                        {i.desktop ? <Monitor className="size-4" /> : <SquareTerminal className="size-4" />}
                        {i.displayName || i.name}
                      </div>
                      {i.description && <p className="text-sm text-base-content/60 line-clamp-2">{i.description}</p>}
                      <code className="font-mono-tight mt-1 truncate text-base-content/40">{i.image}</code>
                    </button>
                  ))}
                </div>
              </div>
            ))}
          {usable.length === 0 && (
            <p className="text-sm text-base-content/60">
              No images available. <Link to="/images" className="link">Add one</Link>.
            </p>
          )}
        </section>

        <section className="rounded-box bg-base-100 p-4 sm:p-5">
          <div className="grid gap-x-6 md:grid-cols-2">
            <Field label="Name" hint="Lowercase letters, digits and '-', up to 40 characters. Used for SSH: ssh <name>@gateway.">
              <input
                className={cx("input w-full", name && !nameOK && "input-error")}
                value={name}
                onChange={(e) => {
                  setName(e.target.value);
                  setNameTouched(true);
                }}
                required
              />
            </Field>
            <Field label="Class" hint={klass ? `${networkLabel[klass.networkMode] ?? klass.networkMode} · ${klass.storageMode} storage` : undefined}>
              <select className="select w-full" value={cls} onChange={(e) => setCls(e.target.value)}>
                {classes.data?.map((c) => (
                  <option key={c.name} value={c.name}>
                    {c.name} · {networkLabel[c.networkMode] ?? c.networkMode}
                  </option>
                ))}
              </select>
            </Field>
          </div>
          {klass && img && (
            <div className="mt-4">
              <ResourceInputs
                className={cls}
                value={res}
                defaults={{ ...img.defaultResources, disk: img.defaultResources.disk ?? klass.defaultDisk }}
                onChange={setRes}
                persistent={klass.storageMode === "persistent"}
              />
            </div>
          )}
        </section>

        <section className="rounded-box bg-base-100">
          <button type="button" className="flex w-full flex-wrap items-center gap-2 p-4 text-left font-semibold sm:p-5" onClick={() => setAdvanced((a) => !a)}>
            <ChevronDown className={cx("size-4 transition-transform", !advanced && "-rotate-90")} /> Advanced
            <span className="text-sm font-normal text-base-content/50">sharing, password, extra keys{klass?.networkMode === "loadBalancer" ? ", ports" : ""}</span>
          </button>
          {advanced && (
            <div className="space-y-5 px-4 pb-4 sm:px-5 sm:pb-5">
              <div>
                <h3 className="mb-1 flex items-center gap-2 text-sm font-medium">
                  <User className="size-4" /> Share with
                </h3>
                <SharingInput value={sharing} onChange={setSharing} />
              </div>
              <div className="grid gap-x-6 md:grid-cols-2">
                <Field label={<span className="flex items-center gap-2"><Lock className="size-4" /> Password for user dev</span>} hint="Optional. Enables SSH password login (also needed for sudo prompts in some tools).">
                  <input className="input w-full" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
                </Field>
                <Field label="Extra SSH public keys" hint="Your keys and the keys of users you share with always work. Add deploy or CI keys here.">
                  <textarea className="textarea font-mono-tight w-full" rows={2} value={keys} onChange={(e) => setKeys(e.target.value)} placeholder="ssh-ed25519 AAAA… ci@example" />
                </Field>
              </div>
              {klass?.networkMode === "loadBalancer" && (
                <div>
                  <h3 className="mb-2 flex items-center gap-2 text-sm font-medium">
                    <Network className="size-4" /> Ports on the environment address
                  </h3>
                  <PortsInput value={ports} onChange={setPorts} />
                </div>
              )}
              {me.data?.admin && (
                <Field label="Owner" hint="Administrators can create environments for other users.">
                  <select className="select w-full max-w-xs" value={owner} onChange={(e) => setOwner(e.target.value)}>
                    <option value="">me ({me.data.name})</option>
                    {users.data
                      ?.filter((u) => u.name !== me.data?.name)
                      .map((u) => (
                        <option key={u.name} value={u.name}>
                          {u.name}
                          {u.displayName ? ` (${u.displayName})` : ""}
                        </option>
                      ))}
                  </select>
                </Field>
              )}
            </div>
          )}
        </section>

        <ErrorAlert error={create.error} />
        <div className="sticky bottom-0 -mx-3 flex justify-end gap-2 border-t border-base-300 bg-base-200/95 px-3 py-3 backdrop-blur sm:static sm:mx-0 sm:border-0 sm:bg-transparent sm:p-0">
          <Link to="/" className="btn btn-ghost">
            Cancel
          </Link>
          <button className="btn btn-primary max-sm:flex-1" disabled={!nameOK || !image || create.isPending}>
            {create.isPending && <Spinner className="loading-xs" />} Create and open
          </button>
        </div>
      </form>
    </>
  );
}
