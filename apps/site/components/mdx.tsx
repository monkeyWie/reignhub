import { ArchitectureDiagram } from "./architecture-diagram";
import defaultMdxComponents from "fumadocs-ui/mdx";
import { Card } from "fumadocs-ui/components/card";
import type { ComponentProps, ComponentType } from "react";
import type { MDXComponents } from "mdx/types";
import { localizeHref, type Locale } from "../lib/i18n";

export function getMDXComponents(
  components?: MDXComponents,
  locale: Locale = "en",
) {
  const Anchor = (components?.a ??
    defaultMdxComponents.a ??
    "a") as ComponentType<ComponentProps<"a">>;
  return {
    ...defaultMdxComponents,
    ...components,
    ArchitectureDiagram: (
      props: Omit<ComponentProps<typeof ArchitectureDiagram>, "locale">,
    ) => <ArchitectureDiagram {...props} locale={locale} />,
    a: ({ href, ...props }: ComponentProps<"a">) =>
      href && /^\/(?:web|chat)(?:[/?#]|$)/.test(href) ? (
        <a {...props} href={href} />
      ) : (
        <Anchor {...props} href={href ? localizeHref(href, locale) : href} />
      ),
    Card: ({ href, ...props }: ComponentProps<typeof Card>) => (
      <Card {...props} href={href ? localizeHref(href, locale) : href} />
    ),
  } satisfies MDXComponents;
}
export const useMDXComponents = getMDXComponents;
declare global {
  type MDXProvidedComponents = ReturnType<typeof getMDXComponents>;
}
