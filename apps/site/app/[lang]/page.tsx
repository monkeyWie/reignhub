import { pageMetadata, marketingMeta, siteOrigin } from "../../lib/seo";
import { translate, isLocale, localizeHref } from "../../lib/i18n";
import { notFound } from "next/navigation";
import { LocalizedLink as Link } from "../../components/locale";
import { HeroDownload } from "../../components/hero-download";
import { WorkspaceStories } from "../../components/workspace-stories";
import { WorkspaceDemo } from "../../components/workspace-demo";
import { SiteShell } from "../../components/site";
import "./home.css";
export default async function Home({
  params,
}: {
  params: Promise<{ lang: string }>;
}) {
  const { lang } = await params;
  if (!isLocale(lang)) notFound();
  const t = translate(lang);
  return (
    <SiteShell>
      <main id="main" className="atlas-home">
        {siteOrigin && (
          <script
            type="application/ld+json"
            dangerouslySetInnerHTML={{
              __html: JSON.stringify({
                "@context": "https://schema.org",
                "@type": "WebSite",
                name: "ReignHub",
                url: siteOrigin + localizeHref("/", lang),
                description: marketingMeta["/"][lang][1],
                inLanguage: lang,
              }).replace(/</g, "\\u003c"),
            }}
          />
        )}
        <section className="atlas-hero site-width">
          <div className="atlas-intro">
            <h1>
              {t("电脑上的 Agent，")}
              <br />
              <em>{t("手机上接着聊。")}</em>
            </h1>
            <div className="atlas-pitch">
              <p>
                {t(
                  "Codex、Claude Code 和 Pi 在你的电脑上运行。出门后，用手机查看进度、补充指令，或打开项目终端。",
                )}
              </p>
              <HeroDownload />
            </div>
          </div>
          <WorkspaceDemo locale={lang} />
        </section>
        <section
          className="site-width native-metrics"
          aria-label={t("原生 UI 性能")}
        >
          <dl>
            {[
              ["≈ 10.4 MiB", t("macOS 安装包")],
              ["≈ 54 MiB", t("空闲内存")],
              ["≈ 774 ms", t("进程冷启动")],
              ["6", t("支持平台")],
            ].map(([v, k]) => (
              <div key={k}>
                <dt>{k}</dt>
                <dd>{v}</dd>
              </div>
            ))}
          </dl>
          <Link href="/docs/architecture/performance">
            {t("原生 UI · Apple M5 实测")} ↗
          </Link>
          <span>macOS · Windows · Linux · iOS · Android · Web</span>
        </section>
        <section id="features" className="atlas-work site-width">
          <WorkspaceStories locale={lang} />
        </section>
      </main>
    </SiteShell>
  );
}
export async function generateMetadata({
  params,
}: {
  params: Promise<{ lang: string }>;
}) {
  const { lang } = await params;
  if (!isLocale(lang)) notFound();
  const [title, description] = marketingMeta["/"][lang];
  return pageMetadata(lang, "/", title, description);
}
