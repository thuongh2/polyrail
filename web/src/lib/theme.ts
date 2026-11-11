// Theme: follows the system unless the user picked one (stored per browser).
export type Theme = "light" | "dark" | "system";

export function getTheme(): Theme {
  try {
    return (localStorage.getItem("theme") as Theme) || "system";
  } catch {
    return "system";
  }
}

export function applyTheme(t: Theme = getTheme()) {
  const dark = t === "dark" || (t === "system" && matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
}

export function setTheme(t: Theme) {
  try {
    localStorage.setItem("theme", t);
  } catch {
    /* private mode: theme just won't persist */
  }
  applyTheme(t);
}
