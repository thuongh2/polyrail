import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { applyTheme } from "@/lib/theme";
import { App } from "./App";

applyTheme();
matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => applyTheme());
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
