"use client";
import NextLink from "next/link";
import {
  createContext,
  useContext,
  type ComponentProps,
  type ReactNode,
} from "react";
import { localizeHref, translate, type Locale } from "../lib/i18n";
const LocaleContext = createContext<Locale>("en");
export function LocaleProvider({
  locale,
  children,
}: {
  locale: Locale;
  children: ReactNode;
}) {
  return (
    <LocaleContext.Provider value={locale}>{children}</LocaleContext.Provider>
  );
}
export function useLocale() {
  return useContext(LocaleContext);
}
export function useT() {
  return translate(useLocale());
}
export function LocalizedLink({
  href,
  ...props
}: ComponentProps<typeof NextLink>) {
  const locale = useLocale();
  if (typeof href === "string" && /^\/(?:web|chat)(?:[/?#]|$)/.test(href)) {
    const { prefetch, replace, scroll, onNavigate, ...anchor } = props;
    return <a {...anchor} href={href} />;
  }
  return (
    <NextLink
      {...props}
      href={typeof href === "string" ? localizeHref(href, locale) : href}
    />
  );
}
