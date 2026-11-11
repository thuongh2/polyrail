// Small building blocks shared by the management pages (providers, models, orgs, users, MCP).
import { useId, useState, type FormEvent, type ReactNode } from "react";
import { AlertCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter,
  AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { toast } from "@/components/toast";
import { cn } from "@/lib/utils";

/** A labelled form control with an optional hint. `children` receives the control's id. */
export function Field({
  label, hint, className, children,
}: { label: string; hint?: ReactNode; className?: string; children: (id: string, hintId?: string) => ReactNode }) {
  const id = useId();
  const hintId = hint ? id + "-hint" : undefined;
  return (
    <div className={cn("grid content-start gap-1.5", className)}>
      <Label htmlFor={id}>{label}</Label>
      {children(id, hintId)}
      {hint && (
        <p id={hintId} className="text-xs text-muted-foreground">
          {hint}
        </p>
      )}
    </div>
  );
}

export function FormError({ message }: { message?: string }) {
  if (!message) return null;
  return (
    <div role="alert" className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
      <AlertCircle className="mt-0.5 size-4 shrink-0" aria-hidden />
      <span>{message}</span>
    </div>
  );
}

/**
 * Dialog holding one form. onSubmit throws to show the error inline and keep the dialog open;
 * on success the dialog closes and `success` is toasted.
 */
export function FormDialog({
  open, onOpenChange, title, description, submitLabel, success, onSubmit, wide, children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: ReactNode;
  submitLabel: string;
  success: string;
  onSubmit: () => Promise<unknown>;
  wide?: boolean;
  children: ReactNode;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const change = (o: boolean) => {
    if (busy) return;
    if (!o) setError(undefined);
    onOpenChange(o);
  };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await onSubmit();
      toast(success);
      onOpenChange(false);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogContent className={cn("max-h-[calc(100dvh-2rem)] overflow-y-auto scrollbar-thin", wide ? "sm:max-w-2xl" : "sm:max-w-lg")}>
        <form onSubmit={submit} className="grid gap-4" noValidate={false}>
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            {description && <DialogDescription>{description}</DialogDescription>}
          </DialogHeader>
          {children}
          <FormError message={error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => change(false)} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy}>
              {busy ? "Saving…" : submitLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Confirmation for a destructive action. Stays open and shows the error if the action fails.
 * `success` may be a function of onConfirm's result, for toasts that report what happened. */
export function ConfirmDialog<T>({
  open, onOpenChange, title, description, confirmLabel, success, onConfirm, destructive = true,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: ReactNode;
  confirmLabel: string;
  success: string | ((result: T) => string);
  onConfirm: () => Promise<T>;
  destructive?: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const run = async () => {
    setBusy(true);
    setError(undefined);
    try {
      const result = await onConfirm();
      toast(typeof success === "function" ? success(result) : success);
      onOpenChange(false);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <AlertDialog
      open={open}
      onOpenChange={(o) => {
        if (busy) return;
        if (!o) setError(undefined);
        onOpenChange(o);
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <FormError message={error} />
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <Button variant={destructive ? "destructive" : "default"} onClick={run} disabled={busy}>
            {busy ? "Working…" : confirmLabel}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

/** Enabled / disabled state as text with a status colour (never a bare dot). */
export function EnabledText({ on, onLabel = "Enabled", offLabel = "Disabled" }: { on: boolean; onLabel?: string; offLabel?: string }) {
  return <span className={on ? "text-success" : "text-muted-foreground"}>{on ? onLabel : offLabel}</span>;
}

/** Splits a comma or newline separated list, trimming blanks. */
export const splitList = (s: string) =>
  s
    .split(/[\n,]/)
    .map((x) => x.trim())
    .filter(Boolean);

/** Shown in place of a page the current role cannot use. */
export function NoAccess({ children }: { children: ReactNode }) {
  return <p className="rounded-lg border px-4 py-6 text-sm text-muted-foreground">{children}</p>;
}
