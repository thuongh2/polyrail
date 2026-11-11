import { useState } from "react";
import { logoFor, modelAuthor, providerLabel } from "@/lib/providers";
import { cn } from "@/lib/utils";

type Props = {
  /** A provider type (openai, bedrock, ...). */
  type?: string;
  /** A model id: its author prefix before "/" picks the logo (OpenRouter style). */
  model?: string;
  /** Fallback author slug when the model id has no prefix (e.g. owned_by). */
  author?: string;
  size?: 16 | 20 | 24;
  /** Accessible name; omit when the name is written next to the logo (the logo is then decorative). */
  label?: string;
  className?: string;
};

/** A provider or model-author logo. One-color logos follow the text color; unknown ones get a monogram. */
export function ProviderLogo({ type, model, author, size = 20, label, className }: Props) {
  const key = type ?? (model && modelAuthor(model)) ?? "";
  const logo = logoFor(key) ?? (author ? logoFor(author) : null);
  const [broken, setBroken] = useState(false);
  const a11y = label ? { role: "img", "aria-label": label } : { "aria-hidden": true };
  const box = { width: size, height: size };
  const cls = cn("inline-block shrink-0 align-middle", className);

  if (logo && !broken) {
    if (logo.colored) return <img src={logo.src} alt={label ?? ""} aria-hidden={label ? undefined : true} width={size} height={size} className={cls} onError={() => setBroken(true)} />;
    const mask = `url("${logo.src}") center / contain no-repeat`;
    return <span {...a11y} className={cn(cls, "bg-current")} style={{ ...box, mask, WebkitMask: mask }} />;
  }
  const name = (type ? providerLabel(type) : key || author || "?").replace(/[^\p{L}\p{N}]/gu, "");
  return (
    <span
      {...a11y}
      className={cn(cls, "grid place-items-center rounded-[22%] bg-muted font-semibold leading-none text-muted-foreground")}
      style={{ ...box, fontSize: Math.round(size * 0.55) }}
    >
      {(name[0] ?? "?").toUpperCase()}
    </span>
  );
}
