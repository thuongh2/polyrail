import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/toast";
import { writeClipboard } from "@/lib/utils";

/** Icon-only copy button (the label is its accessible name and tooltip). */
export function CopyIcon({ text, label = "Copy" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-xs"
      aria-label={done ? "Copied" : label}
      title={label}
      onClick={async () => {
        try {
          await writeClipboard(text);
          setDone(true);
          setTimeout(() => setDone(false), 1500);
        } catch {
          toast("Copy failed. Select the text and copy it manually.", { error: true });
        }
      }}
    >
      {done ? <Check aria-hidden /> : <Copy aria-hidden />}
    </Button>
  );
}
