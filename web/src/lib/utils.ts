import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/** Copies text; falls back to execCommand where the Clipboard API is missing (plain http on a LAN IP). */
export async function writeClipboard(text: string) {
  if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(text);
  // shortcut: execCommand fallback for non-secure origins; drop when HTTPS is a given.
  const t = document.createElement("textarea");
  t.value = text;
  t.style.position = "fixed";
  t.style.opacity = "0";
  document.body.appendChild(t);
  t.select();
  const ok = document.execCommand("copy");
  t.remove();
  if (!ok) throw new Error("copy failed");
}

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
