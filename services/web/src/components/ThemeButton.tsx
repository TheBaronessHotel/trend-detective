import { useState } from "react";

type Theme = "system" | "light" | "dark";

const order: Theme[] = ["system", "light", "dark"];
const key = "theme";

function stored(): Theme {
  try {
    const t = localStorage.getItem(key);
    return t === "light" || t === "dark" ? t : "system";
  } catch {
    // Private windows and blocked site data throw.
    return "system";
  }
}

/** Cycles system, light and dark. index.html applies the stored one before first paint. */
export function ThemeButton() {
  const [theme, setTheme] = useState<Theme>(stored);

  function next() {
    const t = order[(order.indexOf(theme) + 1) % order.length] ?? "system";
    setTheme(t);
    if (t === "system") document.documentElement.removeAttribute("data-theme");
    else document.documentElement.setAttribute("data-theme", t);
    try {
      localStorage.setItem(key, t);
    } catch {
      // A theme that doesn't persist is still a theme.
    }
  }

  return (
    <button type="button" className="theme" onClick={next} aria-live="polite">
      theme: {theme}
    </button>
  );
}
