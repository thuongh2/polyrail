// TODO(agent): implement. Owned by the public agent.
import type { Scope } from "@/App";
import { PageHeader } from "@/components/states";

export default function Status(_props: { scope?: Scope; id?: string; mode?: "login" | "signup" }) {
  return <PageHeader title="Status" description="Coming soon." />;
}
