import { useEffect, useState } from "react";

// Minimal toast: toast("Saved") from anywhere; <Toaster /> renders them once at the root.
type Item = { id: number; text: string; error?: boolean };
let push: (i: Item) => void = () => {};
let next = 0;

export function toast(text: string, opts: { error?: boolean } = {}) {
  push({ id: next++, text, error: opts.error });
}

export function Toaster() {
  const [items, setItems] = useState<Item[]>([]);
  useEffect(() => {
    push = (i) => {
      setItems((xs) => [...xs, i]);
      setTimeout(() => setItems((xs) => xs.filter((x) => x.id !== i.id)), 4000);
    };
  }, []);
  return (
    <div className="pointer-events-none fixed right-4 bottom-4 z-[100] flex flex-col gap-2" role="status" aria-live="polite">
      {items.map((i) => (
        <div
          key={i.id}
          className={
            "pointer-events-auto max-w-sm rounded-md border px-4 py-3 text-sm shadow-lg " +
            (i.error ? "border-destructive/50 bg-background text-destructive" : "bg-foreground text-background")
          }
        >
          {i.text}
        </div>
      ))}
    </div>
  );
}
