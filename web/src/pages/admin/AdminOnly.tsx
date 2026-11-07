import type { ReactNode } from "react";
import { useMe } from "../../lib/prefs";
import { Loading } from "../../components/ui";

export function AdminOnly({ children }: { children: ReactNode }) {
  const me = useMe();
  if (me.isLoading) return <Loading />;
  if (!me.data?.admin) return <div className="alert alert-warning">Administrator role required.</div>;
  return <>{children}</>;
}
