import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../../lib/api";
import { useInfo, useMe } from "../../lib/prefs";
import { PageHeader } from "../../components/Layout";
import { ErrorAlert, Field, Loading } from "../../components/ui";
import { useToast } from "../../components/toast";
import { UsageSummary } from "../Environments";

export function ProfilePage() {
  const me = useMe();
  const info = useInfo();
  const qc = useQueryClient();
  const toast = useToast();
  const [name, setName] = useState("");
  useEffect(() => setName(me.data?.displayName ?? ""), [me.data?.displayName]);
  const save = useMutation({
    mutationFn: () => api.updateMe({ displayName: name }),
    onSuccess: (m) => {
      qc.setQueryData(["me"], m);
      toast("Saved", "success");
    },
  });
  if (!me.data) return <Loading />;
  const providerName = (id: string) => info.data?.providers.find((p) => p.id === id)?.name ?? id;
  return (
    <>
      <PageHeader title="Profile" />
      <UsageSummary />
      <div className="max-w-2xl space-y-4">
        <section className="rounded-box bg-base-100 p-5">
          <dl className="grid grid-cols-[9rem_1fr] gap-y-2 text-sm">
            <dt className="text-base-content/60">User name</dt>
            <dd className="font-mono">{me.data.name}</dd>
            <dt className="text-base-content/60">E-mail</dt>
            <dd>{me.data.email || "-"}</dd>
            <dt className="text-base-content/60">Role</dt>
            <dd>{me.data.admin ? "Administrator" : "User"}</dd>
            <dt className="text-base-content/60">Signed in with</dt>
            <dd>{me.data.provider ? providerName(me.data.provider) : me.data.method}</dd>
            <dt className="text-base-content/60">Groups</dt>
            <dd className="flex flex-wrap gap-1">
              {me.data.groups.length ? me.data.groups.map((g) => <span key={g} className="badge badge-sm">{g}</span>) : "-"}
            </dd>
            <dt className="text-base-content/60">Linked logins</dt>
            <dd>{me.data.identities.length ? me.data.identities.map((i) => providerName(i.provider)).join(", ") : "-"}</dd>
          </dl>
        </section>
        <section className="rounded-box bg-base-100 p-5">
          <Field label="Display name">
            <div className="flex gap-2">
              <input className="input w-full max-w-sm" value={name} onChange={(e) => setName(e.target.value)} />
              <button className="btn btn-primary" disabled={save.isPending} onClick={() => save.mutate()}>
                Save
              </button>
            </div>
          </Field>
          <ErrorAlert error={save.error} />
        </section>
      </div>
    </>
  );
}
