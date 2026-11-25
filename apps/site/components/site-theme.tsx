"use client";
import { useSyncExternalStore } from "react";
import type { ImgHTMLAttributes } from "react";
import { Sun, Moon } from "lucide-react";
import { useT } from "./locale";
const query = "(prefers-color-scheme: dark)";
function dark() {
  const cls = document.documentElement.classList;
  return (
    cls.contains("dark") ||
    (!cls.contains("light") && matchMedia(query).matches)
  );
}
function subscribe(update: () => void) {
  const observer = new MutationObserver(update);
  observer.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["class"],
  });
  const media = matchMedia(query);
  media.addEventListener("change", update);
  return () => {
    observer.disconnect();
    media.removeEventListener("change", update);
  };
}
export function ThemeButton() {
  const isDark = useSyncExternalStore(subscribe, dark, () => false);
  const t = useT();
  return (
    <button
      type="button"
      className="site-theme-button"
      aria-label={t("切换主题")}
      title={t("切换主题")}
      onClick={() => {
        const theme = dark() ? "light" : "dark";
        const root = document.documentElement;
        root.classList.remove("light", "dark");
        root.classList.add(theme);
        root.style.colorScheme = theme;
        try {
          localStorage.setItem("theme", theme);
        } catch {}
      }}
    >
      {isDark ? <Sun size={18} /> : <Moon size={18} />}
    </button>
  );
}
export function ThemedShot({
  base,
  ...props
}: ImgHTMLAttributes<HTMLImageElement> & { base: string }) {
  const mode = useSyncExternalStore(
    subscribe,
    () => (dark() ? "dark" : "light"),
    () => "system",
  );
  return (
    <picture>
      {mode === "system" && (
        <source media={query} srcSet={base + "dark.webp"} />
      )}
      <img
        {...props}
        src={base + (mode === "dark" ? "dark" : "light") + ".webp"}
      />
    </picture>
  );
}
