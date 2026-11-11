import { useEffect, useState, type ReactNode } from "react";
import { Field } from "@/components/console/bits";
import { ErrorState } from "@/components/states";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import type { ModelKind, ModelView } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";

export type Models = ReturnType<typeof useAsync<ModelView[]>>;
export type PanelProps = { models: Models; apiKey: string; keyField: ReactNode };

/** The model picker for one kind; the choice lives in ?model= and falls back to the first model of that kind. */
export function useModelChoice(models: Models, kind: ModelKind) {
  const [model, setModel] = useQueryParam("model", "");
  const list = models.data?.filter((m) => m.kind === kind);
  const chosen = list?.find((m) => m.id === model)?.id ?? list?.[0]?.id ?? "";
  const field = (
    <Field id="pg-model" label="Model">
      {models.error ? (
        <ErrorState error={models.error} onRetry={models.reload} />
      ) : !list ? (
        <Skeleton className="h-9 w-full" />
      ) : list.length === 0 ? (
        <p className="text-sm text-muted-foreground">No {kindNoun[kind]} models are enabled for this organization.</p>
      ) : (
        <Select value={chosen} onValueChange={setModel}>
          <SelectTrigger id="pg-model" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {list.map((m) => (
              <SelectItem key={m.id} value={m.id}>
                {m.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
    </Field>
  );
  return [chosen, field] as const;
}

const kindNoun: Record<ModelKind, string> = {
  chat: "chat",
  embedding: "embedding",
  speech: "text to speech",
  transcription: "speech to text",
  image: "image",
  rerank: "rerank",
  ocr: "OCR",
  video: "video",
};

/** Error message from a failed gateway response (OpenAI error shape), or the status. */
export async function failure(res: Response) {
  const err = await res.json().catch(() => ({}));
  return new Error(err?.error?.message || `Request failed with status ${res.status}`);
}

/** Object URL for a blob, revoked when the blob changes or the component unmounts. */
export function useObjectURL(blob: Blob | null) {
  const [url, setUrl] = useState<string>();
  useEffect(() => {
    if (!blob) {
      setUrl(undefined);
      return;
    }
    const u = URL.createObjectURL(blob);
    setUrl(u);
    return () => URL.revokeObjectURL(u);
  }, [blob]);
  return url;
}

export function Meta({ items }: { items: [string, ReactNode][] }) {
  return (
    <dl className="flex flex-wrap gap-x-3 text-xs text-muted-foreground tabular-nums">
      {items.map(([k, v]) => (
        <div key={k} className="flex gap-1">
          <dt>{k}</dt>
          <dd className="text-foreground">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

export const bytes = (n: number) => (n < 1024 ? `${n} B` : n < 1024 * 1024 ? `${(n / 1024).toFixed(1)} KB` : `${(n / 1024 / 1024).toFixed(2)} MB`);

