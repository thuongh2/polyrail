import { useId, type ReactNode } from "react";
import { TriangleAlert } from "lucide-react";
import { CopyButton } from "@/components/console/bits";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

/** Shows a just-created secret once, with copy; closing needs an explicit click. */
export function SecretDialog({
  open, onClose, title, description, label, secret, warning,
}: { open: boolean; onClose: () => void; title: string; description: ReactNode; label: string; secret: string; warning: ReactNode }) {
  const id = useId();
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent onInteractOutside={(e) => e.preventDefault()}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <div className="flex items-start gap-2 rounded-md border border-warning/50 bg-warning/10 p-3 text-sm">
          <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden />
          <p>{warning}</p>
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={id} className="sr-only">
            {label}
          </Label>
          <Input id={id} readOnly value={secret} className="font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
          <CopyButton text={secret} />
        </div>
        <DialogFooter>
          <Button onClick={onClose}>I have saved it</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
