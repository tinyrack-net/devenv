import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ticket } from "lucide-react";
import { api, type Token } from "../../lib/api";
import { PageHeader } from "../../components/Layout";
import { Confirm, Empty, ErrorAlert, Loading } from "../../components/ui";
import { TokenTable } from "../settings/Tokens";
import { AdminOnly } from "./AdminOnly";

export function AdminTokensPage() {
  const qc = useQueryClient();
  const tokens = useQuery({ queryKey: ["admin", "tokens"], queryFn: () => api.admin.tokens() });
  const [revoke, setRevoke] = useState<Token | null>(null);
  const del = useMutation({
    mutationFn: (id: string) => api.admin.deleteToken(id),
    onSuccess: () => {
      setRevoke(null);
      qc.invalidateQueries({ queryKey: ["admin", "tokens"] });
    },
  });
  return (
    <AdminOnly>
      <PageHeader title="Access tokens" subtitle="Every user's tokens." />
      <ErrorAlert error={tokens.error} />
      {tokens.isLoading && <Loading />}
      {tokens.data?.length === 0 && <Empty icon={<Ticket className="size-10" />} title="No tokens" />}
      {!!tokens.data?.length && <TokenTable tokens={tokens.data} onRevoke={setRevoke} showUser />}
      <Confirm open={!!revoke} onClose={() => setRevoke(null)} title={`Revoke ${revoke?.name} of ${revoke?.user}?`} danger confirmLabel="Revoke" busy={del.isPending} onConfirm={() => revoke && del.mutate(revoke.id)} />
    </AdminOnly>
  );
}
