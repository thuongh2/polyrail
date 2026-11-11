// TODO(agent): implement. Owned by the public agent.
import type { Scope } from "@/App";
import { PageHeader } from "@/components/states";

export default function Login(_props: { scope?: Scope; id?: string; mode?: "login" | "signup" }) {
  return <PageHeader title="Login" description="Coming soon." />;
}
