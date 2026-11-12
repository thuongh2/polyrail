import { lazy, Suspense, type ComponentType } from "react";
import { Redirect, Route, Switch } from "wouter";
import { SessionProvider } from "@/lib/session";
import { Toaster } from "@/components/toast";
import { LoadingRows } from "@/components/states";
import { PublicLayout } from "@/components/layout/PublicLayout";

// Public pages load eagerly-ish (small); the console is split so catalog visitors never download it.
const Home = lazy(() => import("@/pages/public/Home"));
const Models = lazy(() => import("@/pages/public/Models"));
const ModelDetail = lazy(() => import("@/pages/public/ModelDetail"));
const Status = lazy(() => import("@/pages/public/Status"));
const Docs = lazy(() => import("@/pages/public/Docs"));
const Login = lazy(() => import("@/pages/public/Login"));

const ConsoleLayout = lazy(() => import("@/components/layout/ConsoleLayout").then((m) => ({ default: m.ConsoleLayout })));
const Activity = lazy(() => import("@/pages/console/Activity"));
const Logs = lazy(() => import("@/pages/console/Logs"));
const Playground = lazy(() => import("@/pages/console/Playground"));
const Connect = lazy(() => import("@/pages/console/Connect"));
const Keys = lazy(() => import("@/pages/console/Keys"));
const Credits = lazy(() => import("@/pages/console/Credits"));
const Members = lazy(() => import("@/pages/console/Members"));
const Workspaces = lazy(() => import("@/pages/console/Workspaces"));
const Settings = lazy(() => import("@/pages/console/Settings"));
const Audit = lazy(() => import("@/pages/console/Audit"));
const Webhooks = lazy(() => import("@/pages/console/Webhooks"));
const Policies = lazy(() => import("@/pages/console/Policies"));
const Presets = lazy(() => import("@/pages/console/Presets"));
const KnowledgeBases = lazy(() => import("@/pages/console/KnowledgeBases"));
const KnowledgeBase = lazy(() => import("@/pages/console/KnowledgeBase"));
const Account = lazy(() => import("@/pages/console/Account"));
const ModelsAdmin = lazy(() => import("@/pages/platform/ModelsAdmin"));
const ProvidersAdmin = lazy(() => import("@/pages/platform/ProvidersAdmin"));
const ProviderDetail = lazy(() => import("@/pages/platform/ProviderDetail"));
const Alerts = lazy(() => import("@/pages/console/Alerts"));
const Orgs = lazy(() => import("@/pages/platform/Orgs"));
const Users = lazy(() => import("@/pages/platform/Users"));
const MCP = lazy(() => import("@/pages/platform/MCP"));
const Proxies = lazy(() => import("@/pages/platform/Proxies"));
const SSO = lazy(() => import("@/pages/console/SSO"));
const Observability = lazy(() => import("@/pages/console/Observability"));

/** scope: "org" pages act on the current org; "platform" pages on public resources (operators). */
export type Scope = "org" | "platform";

const page = (C: ComponentType<{ scope?: Scope }>, scope?: Scope) => () => <C scope={scope} />;

function Fallback() {
  return (
    <div className="p-8">
      <LoadingRows rows={3} />
    </div>
  );
}

export function App() {
  return (
    <SessionProvider>
        <Suspense fallback={<Fallback />}>
          <Switch>
            <Route path="/login">{() => <Login mode="login" />}</Route>
            <Route path="/signup">{() => <Login mode="signup" />}</Route>
            {/* The layout sits outside the nested router so its links and active state use absolute
                /app/... paths; pages inside the nest link relative to /app. */}
            <Route path="/app/*?">
              <ConsoleLayout>
                <Route path="/app" nest>
                  <Suspense fallback={<Fallback />}>
                    <Switch>
                      <Route path="/" component={() => <Redirect to="/activity" />} />
                      <Route path="/activity" component={page(Activity, "org")} />
                      <Route path="/logs" component={page(Logs)} />
                      <Route path="/playground" component={page(Playground)} />
                      <Route path="/connect" component={page(Connect)} />
                      <Route path="/keys" component={page(Keys)} />
                      <Route path="/credits" component={page(Credits)} />
                      <Route path="/models" component={page(ModelsAdmin, "org")} />
                      <Route path="/providers" component={page(ProvidersAdmin, "org")} />
                      <Route path="/providers/:id">{(p) => <ProviderDetail scope="org" id={decodeURIComponent(p.id)} />}</Route>
                      <Route path="/alerts" component={page(Alerts, "org")} />
                      <Route path="/members" component={page(Members)} />
                      <Route path="/workspaces" component={page(Workspaces)} />
                      <Route path="/settings" component={page(Settings)} />
                      <Route path="/webhooks" component={page(Webhooks)} />
                      <Route path="/policies" component={page(Policies)} />
                      <Route path="/presets" component={page(Presets)} />
                      <Route path="/kbs" component={page(KnowledgeBases)} />
                      <Route path="/kbs/:id">{(p) => <KnowledgeBase id={decodeURIComponent(p.id)} />}</Route>
                      <Route path="/sso" component={page(SSO)} />
                      <Route path="/observability" component={page(Observability, "org")} />
                      <Route path="/audit" component={page(Audit, "org")} />
                      <Route path="/account" component={page(Account)} />
                      <Route path="/platform/activity" component={page(Activity, "platform")} />
                      <Route path="/platform/orgs" component={page(Orgs)} />
                      <Route path="/platform/providers" component={page(ProvidersAdmin, "platform")} />
                      <Route path="/platform/providers/:id">{(p) => <ProviderDetail scope="platform" id={decodeURIComponent(p.id)} />}</Route>
                      <Route path="/platform/alerts" component={page(Alerts, "platform")} />
                      <Route path="/platform/models" component={page(ModelsAdmin, "platform")} />
                      <Route path="/platform/users" component={page(Users)} />
                      <Route path="/platform/mcp" component={page(MCP)} />
                      <Route path="/platform/proxies" component={page(Proxies)} />
                      <Route path="/platform/observability" component={page(Observability, "platform")} />
                      <Route path="/platform/audit" component={page(Audit, "platform")} />
                      <Route>{() => <NotFound />}</Route>
                    </Switch>
                  </Suspense>
                </Route>
              </ConsoleLayout>
            </Route>
            <Route>
              <PublicLayout>
                <Suspense fallback={<Fallback />}>
                  <Switch>
                    <Route path="/" component={page(Home)} />
                    <Route path="/models" component={page(Models)} />
                    <Route path="/models/*">{(p) => <ModelDetail id={p["*"]} />}</Route>
                    <Route path="/status" component={page(Status)} />
                    <Route path="/docs" component={page(Docs)} />
                    <Route>{() => <NotFound />}</Route>
                  </Switch>
                </Suspense>
              </PublicLayout>
            </Route>
          </Switch>
        </Suspense>
        <Toaster />
    </SessionProvider>
  );
}

function NotFound() {
  return (
    <div className="py-16 text-center">
      <h1 className="text-2xl font-semibold">Page not found</h1>
      <p className="mt-2 text-muted-foreground">
        <a className="underline" href="/">Go to the start</a>
      </p>
    </div>
  );
}
