// TODO(agent): implement. Owned by the console agent.
import type { Scope } from "@/App";
import { PageHeader } from "@/components/states";

export default function Activity(_props: { scope?: Scope; id?: string; mode?: "login" | "signup" }) {
  return <PageHeader title="Activity" description="Coming soon." />;
}
