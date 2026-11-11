// TODO(agent): implement. Owned by the platform agent.
import type { Scope } from "@/App";
import { PageHeader } from "@/components/states";

export default function MCP(_props: { scope?: Scope; id?: string; mode?: "login" | "signup" }) {
  return <PageHeader title="MCP" description="Coming soon." />;
}
