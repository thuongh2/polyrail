import type { ReactNode } from "react";
import { AlertCircle, Inbox } from "lucide-react";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { ApiError } from "@/lib/api";

/** Skeleton in the shape of a table: shown while a region loads. */
export function LoadingRows({ rows = 4 }: { rows?: number }) {
  return (
    <div className="space-y-2" aria-busy="true" aria-label="Loading">
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-10 w-full" />
      ))}
    </div>
  );
}

export function EmptyState({ title, children, action }: { title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed px-6 py-12 text-center">
      <Inbox className="size-6 text-muted-foreground" aria-hidden />
      <p className="font-medium">{title}</p>
      {children && <div className="max-w-md text-sm text-muted-foreground">{children}</div>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}

export function ErrorState({ error, onRetry }: { error: Error; onRetry?: () => void }) {
  const signedOut = error instanceof ApiError && error.status === 401;
  return (
    <div role="alert" className="flex items-start gap-3 rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm">
      <AlertCircle className="mt-0.5 size-4 text-destructive" aria-hidden />
      <div className="flex-1">
        <p className="font-medium">{signedOut ? "Your session ended" : "Could not load this"}</p>
        <p className="text-muted-foreground">{signedOut ? "Sign in again to continue." : error.message}</p>
      </div>
      {signedOut ? (
        <Button size="sm" variant="outline" asChild>
          <a href="/login">Sign in</a>
        </Button>
      ) : (
        onRetry && (
          <Button size="sm" variant="outline" onClick={onRetry}>
            Try again
          </Button>
        )
      )}
    </div>
  );
}

/** Renders the loading, error, empty and full states of one data region. */
export function DataState<T>({
  state,
  empty,
  isEmpty = (d) => Array.isArray(d) && d.length === 0,
  rows,
  children,
}: {
  state: { data?: T; error?: Error; loading: boolean; reload: () => void };
  empty?: ReactNode;
  isEmpty?: (d: T) => boolean;
  rows?: number;
  children: (data: T) => ReactNode;
}) {
  if (state.error) return <ErrorState error={state.error} onRetry={state.reload} />;
  if (state.data === undefined) return <LoadingRows rows={rows} />;
  if (empty && isEmpty(state.data)) return <>{empty}</>;
  return <>{children(state.data)}</>;
}

export function PageHeader({ title, description, actions }: { title: string; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
      <div className="min-w-0">
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="mt-1 max-w-2xl text-sm text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex gap-2">{actions}</div>}
    </div>
  );
}
