import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { ApiError, authApi, type AuthConfig, type OrgRef, type Role, type User, type Workspace } from "@/lib/api";

const rank: Record<Role, number> = { viewer: 1, member: 2, admin: 3, owner: 4 };
export const roleAtLeast = (r: Role | undefined, min: Role) => !!r && rank[r] >= rank[min];

/** The "All workspaces" choice of org admins (list pages show every workspace). */
export const ALL_WS = "all";

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
  /** Workspaces of the current org the user is in, default first. */
  workspaces: Workspace[];
  /** The current workspace; null when an org admin chose "All workspaces". */
  ws: Workspace | null;
  /** Current workspace id, or ALL_WS. */
  wsId: string;
  setWs: (id: string) => void;
  /** Whether "All workspaces" may be chosen (org owners and admins). */
  canAllWs: boolean;
  /** Role in the current workspace is at least `min` (org role under "All workspaces"; operators always). */
  wsCan: (min: Role) => boolean;
  /** path with ?workspace=<current> added (unchanged under "All workspaces"). */
  wsList: (path: string) => string;
  /** Create-body field for the current workspace ({} under "All workspaces": the server uses the default). */
  wsBody: { workspace_id?: string };
  /** Display name of a workspace id from a response (absent = default). */
  wsName: (id?: string) => string;
};

const Ctx = createContext<Session | null>(null);

const wsKey = (org: string) => `console.ws.${org}`;
function readWs(org: string) {
  try {
    return localStorage.getItem(wsKey(org)) ?? "";
  } catch {
    return "";
  }
}
function writeWs(org: string, id: string) {
  try {
    localStorage.setItem(wsKey(org), id);
  } catch {
    /* private mode: the choice just won't persist */
  }
}

/** Mirrors the workspace into ?ws= on the current URL (absent for the default workspace). */
export function syncWsQuery(id: string) {
  const p = new URLSearchParams(location.search);
  if (id && id !== "default") p.set("ws", id);
  else p.delete("ws");
  const qs = p.toString();
  const next = location.pathname + (qs ? "?" + qs : "") + location.hash;
  if (next !== location.pathname + location.search + location.hash) history.replaceState(history.state, "", next);
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [orgs, setOrgs] = useState<OrgRef[]>([]);
  const [orgId, setOrgId] = useState<string>(() => sessionStorage.getItem("org") ?? "");
  const [config, setConfig] = useState<AuthConfig | null>(null);
  const [loading, setLoading] = useState(true);
  // Chosen workspace per org; "" = not chosen yet in this tab (URL ?ws=, then localStorage, then default).
  const [wsPick, setWsPick] = useState<Record<string, string>>({});

  const refresh = useCallback(async () => {
    try {
      const me = await authApi.session();
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

  const workspaces = org?.workspaces ?? [];
  const canAllWs = roleAtLeast(org?.role, "admin");
  const valid = (id: string) => (id === ALL_WS ? canAllWs : workspaces.some((w) => w.id === id));
  let wsId = "default";
  if (org) {
    const fromURL = new URLSearchParams(location.search).get("ws") ?? "";
    const cands = [wsPick[org.id] ?? "", fromURL, readWs(org.id)];
    wsId = cands.find((c) => c && valid(c)) ?? workspaces[0]?.id ?? "default";
  }
  const ws = wsId === ALL_WS ? null : (workspaces.find((w) => w.id === wsId) ?? null);
  // Remember the resolved choice (e.g. from a ?ws= link) so later pages without ?ws= keep it.
  const orgKey = org?.id ?? "";
  useEffect(() => {
    if (orgKey) writeWs(orgKey, wsId);
  }, [orgKey, wsId]);
  const setWs = (id: string) => {
    if (!org) return;
    writeWs(org.id, id);
    setWsPick((p) => ({ ...p, [org.id]: id }));
    syncWsQuery(id);
  };
  const wsCan = (min: Role) => !!user && (user.operator || (ws ? roleAtLeast(ws.role, min) : can(min)));
  const wsList = (path: string) => (ws ? path + (path.includes("?") ? "&" : "?") + "workspace=" + encodeURIComponent(ws.id) : path);
  const wsBody = ws ? { workspace_id: ws.id } : {};
  const wsName = (id?: string) => {
    const w = workspaces.find((x) => (id ? x.id === id : x.is_default));
    return w?.name ?? (id || "Default");
  };

  return (
    <Ctx.Provider value={{ loading, user, orgs, org, config, setOrg, refresh, can, workspaces, ws, wsId, setWs, canAllWs, wsCan, wsList, wsBody, wsName }}>
      {children}
    </Ctx.Provider>
  );
}

export function useSession() {
  const s = useContext(Ctx);
  if (!s) throw new Error("useSession outside SessionProvider");
  return s;
}
