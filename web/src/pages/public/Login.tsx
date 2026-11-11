import { useEffect, useState, type FormEvent } from "react";
import { Link, useLocation } from "wouter";
import { AlertCircle, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Wordmark } from "@/components/layout/PublicLayout";
import { ThemeToggle } from "@/components/layout/ThemeToggle";
import { authApi } from "@/lib/api";
import { useSession } from "@/lib/session";

/** Only same-origin paths, so ?next= can't send people to another site. */
function nextPath() {
  const n = new URLSearchParams(location.search).get("next") ?? "";
  return n.startsWith("/") && !n.startsWith("//") && !n.startsWith("/\\") ? n : "/app";
}

export default function Login({ mode }: { mode: "login" | "signup" }) {
  const { user, loading, config, refresh } = useSession();
  const [, navigate] = useLocation();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const signup = mode === "signup";
  const qs = location.search;

  useEffect(() => {
    if (!loading && user) navigate(nextPath(), { replace: true });
  }, [loading, user, navigate]);

  useEffect(() => {
    document.title = signup ? "Create account – agent-gateway" : "Sign in – agent-gateway";
  }, [signup]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    setBusy(true);
    try {
      if (signup) await authApi.signup(email.trim(), password, name.trim());
      else await authApi.login(email.trim(), password);
      await refresh();
      navigate(nextPath());
    } catch (err) {
      setError(err instanceof Error ? err.message : "Something went wrong");
    } finally {
      setBusy(false);
    }
  }

  const signupClosed = signup && config && !config.signup;

  return (
    <div className="flex min-h-dvh flex-col bg-muted/40">
      <header className="flex h-14 items-center justify-between px-4">
        <Wordmark />
        <ThemeToggle />
      </header>
      <main className="flex flex-1 items-start justify-center px-4 pt-[10vh] pb-16">
        <div className="w-full max-w-sm rounded-xl border bg-background p-6 shadow-sm sm:p-8">
          <h1 className="text-xl font-semibold tracking-tight">{signup ? "Create your account" : "Sign in"}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {signup ? "You get your own organization and can create API keys right away." : "Use your agent-gateway account."}
          </p>

          {signupClosed ? (
            <p className="mt-6 text-sm text-muted-foreground">
              Sign-up is turned off on this gateway. Ask an operator to invite you, then{" "}
              <Link href={`/login${qs}`} className="text-foreground underline underline-offset-4">
                sign in
              </Link>
              .
            </p>
          ) : (
            <>
              {config?.sso && (
                <>
                  <Button asChild variant="outline" className="mt-6 w-full">
                    <a href="/api/auth/oidc/login">Continue with SSO</a>
                  </Button>
                  <div className="my-5 flex items-center gap-3 text-xs text-muted-foreground">
                    <span className="h-px flex-1 bg-border" />
                    or with email
                    <span className="h-px flex-1 bg-border" />
                  </div>
                </>
              )}
              <form onSubmit={submit} className={config?.sso ? "space-y-4" : "mt-6 space-y-4"}>
                {signup && (
                  <div className="space-y-1.5">
                    <Label htmlFor="name">Name</Label>
                    <Input id="name" autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} />
                  </div>
                )}
                <div className="space-y-1.5">
                  <Label htmlFor="email">Email</Label>
                  <Input
                    id="email"
                    type="email"
                    autoComplete="email"
                    required
                    autoFocus
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                    aria-invalid={!!error || undefined}
                    aria-describedby={error ? "login-error" : undefined}
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="password">Password</Label>
                  <Input
                    id="password"
                    type="password"
                    autoComplete={signup ? "new-password" : "current-password"}
                    required
                    minLength={signup ? 10 : undefined}
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    aria-invalid={!!error || undefined}
                    aria-describedby={[error && "login-error", signup && "pw-hint"].filter(Boolean).join(" ") || undefined}
                  />
                  {signup && (
                    <p id="pw-hint" className="text-xs text-muted-foreground">
                      At least 10 characters.
                    </p>
                  )}
                </div>
                {error && (
                  <p id="login-error" role="alert" className="flex items-start gap-2 text-sm text-destructive">
                    <AlertCircle className="mt-0.5 size-4 shrink-0" aria-hidden />
                    {error}
                  </p>
                )}
                <Button type="submit" className="w-full" disabled={busy}>
                  {busy && <Loader2 className="motion-safe:animate-spin" aria-hidden />}
                  {signup ? "Create account" : "Sign in"}
                </Button>
              </form>
            </>
          )}

          <p className="mt-6 text-center text-sm text-muted-foreground">
            {signup ? (
              <>
                Already have an account?{" "}
                <Link href={`/login${qs}`} className="text-foreground underline underline-offset-4">
                  Sign in
                </Link>
              </>
            ) : config?.signup ? (
              <>
                New here?{" "}
                <Link href={`/signup${qs}`} className="text-foreground underline underline-offset-4">
                  Create an account
                </Link>
              </>
            ) : (
              <Link href="/models" className="underline-offset-4 hover:text-foreground hover:underline">
                Browse public models
              </Link>
            )}
          </p>
        </div>
      </main>
    </div>
  );
}
