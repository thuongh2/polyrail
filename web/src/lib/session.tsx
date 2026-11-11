import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { ApiError, authApi, type AuthConfig, type OrgRef, type Role, type User } from "@/lib/api";

const rank: Record<Role, number> = { viewer: 1, member: 2, admin: 3, owner: 4 };

type Session = {
  loading: boolean;
  user: User | null;
  orgs: OrgRef[];
  org: OrgRef | null; // current org
  config: AuthConfig | null;
  setOrg: (id: string) => void;
  refresh: () => Promise<void>;
  /** True when the user's role in the current org is at least `min` (operators always). */
  can: (min: Role) => boolean;
};

const Ctx = createContext<Session | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [orgs, setOrgs] = useState<OrgRef[]>([]);
  const [orgId, setOrgId] = useState<string>(() => sessionStorage.getItem("org") ?? "");
  const [config, setConfig] = useState<AuthConfig | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    try {
      const me = await authApi.me();
      setUser(me.user);
      setOrgs(me.orgs);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) throw e;
      setUser(null);
      setOrgs([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    authApi.config().then(setConfig, () => {});
    refresh().catch(() => setLoading(false));
  }, [refresh]);

  const org = orgs.find((o) => o.id === orgId) ?? orgs[0] ?? null;
  const setOrg = (id: string) => {
    sessionStorage.setItem("org", id);
    setOrgId(id);
  };
  const can = (min: Role) => !!user && (user.operator || (!!org && rank[org.role] >= rank[min]));

  return <Ctx.Provider value={{ loading, user, orgs, org, config, setOrg, refresh, can }}>{children}</Ctx.Provider>;
}

export function useSession() {
  const s = useContext(Ctx);
  if (!s) throw new Error("useSession outside SessionProvider");
  return s;
}
