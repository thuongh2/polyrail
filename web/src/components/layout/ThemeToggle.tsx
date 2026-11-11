import { useState } from "react";
import { Monitor, Moon, Sun } from "lucide-react";
import { Button } from "@/components/ui/button";
import { getTheme, setTheme, type Theme } from "@/lib/theme";

const next: Record<Theme, Theme> = { system: "light", light: "dark", dark: "system" };
const icon = { system: Monitor, light: Sun, dark: Moon };

export function ThemeToggle() {
  const [t, setT] = useState<Theme>(getTheme);
  const Icon = icon[t];
  return (
    <Button
      variant="ghost"
      size="icon"
      aria-label={`Theme: ${t}. Switch to ${next[t]}`}
      title={`Theme: ${t}`}
      onClick={() => {
        setTheme(next[t]);
        setT(next[t]);
      }}
    >
      <Icon />
    </Button>
  );
}
