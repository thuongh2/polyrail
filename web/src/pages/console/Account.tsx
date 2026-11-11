// TODO(agent): implement. Owned by the console agent.
import type { Scope } from "@/App";
import { PageHeader } from "@/components/states";

export default function Account(_props: { scope?: Scope; id?: string; mode?: "login" | "signup" }) {
  return <PageHeader title="Account" description="Coming soon." />;
}
