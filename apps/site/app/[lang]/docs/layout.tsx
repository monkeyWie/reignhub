import { isLocale, translate, localizeHref } from "../../../lib/i18n";
import { notFound } from "next/navigation";
import { DocsProvider } from "../../../components/docs-provider";
import { DocsLayout } from "fumadocs-ui/layouts/docs";
import { source } from "../../../lib/source";
import type { ReactNode } from "react";
import "./docs.css";

export default async function Layout({
  children,
  params,
}: {
  children: ReactNode;
  params: Promise<{ lang: string }>;
}) {
  const { lang } = await params;
  if (!isLocale(lang)) notFound();
  const t = translate(lang);
  return (
    <DocsProvider locale={lang}>
      <DocsLayout
        tree={source.getPageTree(lang)}
        nav={{
          title: (
            <span className="brand">
              ♜ ReignHub <span className="docs-wordmark">/ {t("文档")}</span>
            </span>
          ),
          url: localizeHref("/", lang),
        }}
        githubUrl="https://github.com/monkeyWie/reignhub"
        links={[
          { text: t("特性"), url: localizeHref("/", lang) + "#features" },
          { text: t("技术栈"), url: localizeHref("/technology", lang) },
          { text: t("价格"), url: localizeHref("/pricing", lang) },
          { text: t("打开控制台 ↗"), url: "/web" },
        ]}
      >
        {children}
      </DocsLayout>
    </DocsProvider>
  );
}
