// TODO(agent): implement. Owned by the platform agent.
import type { Scope } from "@/App";
import { PageHeader } from "@/components/states";

export default function ProvidersAdmin(_props: { scope?: Scope; id?: string; mode?: "login" | "signup" }) {
  return <PageHeader title="ProvidersAdmin" description="Coming soon." />;
}
