import { pageMetadata, marketingMeta, siteOrigin } from "../../lib/seo";
import { translate, isLocale, localizeHref } from "../../lib/i18n";
import { notFound } from "next/navigation";
import { LocalizedLink as Link } from "../../components/locale";
import {
  ArrowRight,
  ArrowUpRight,
  Cloud,
  Download,
  Server,
} from "lucide-react";
import { WorkspaceStories } from "../../components/workspace-stories";
import { WorkspaceDemo } from "../../components/workspace-demo";
import { SiteShell } from "../../components/site";
import { PlatformIcon } from "../../components/platform-icon";
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
              <div className="site-actions">
                <Link className="button primary" href="/download">
                  <Download size={16} />
                  {t("下载 ReignHub")}
                </Link>
                <Link className="atlas-inline" href="/web">
                  {t("在浏览器打开")}
                  <ArrowUpRight size={16} />
                </Link>
              </div>
            </div>
          </div>
          <WorkspaceDemo locale={lang} />
        </section>
        <nav
          className="access-platforms site-width"
          aria-label={t("下载客户端")}
        >
          {(
            [
              ["macos", "macOS"],
              ["windows", "Windows"],
              ["linux", "Linux"],
              ["ios", "iOS"],
              ["android", "Android"],
              ["web", "Web"],
            ] as const
          ).map(([id, label]) => (
            <Link key={id} href={id === "web" ? "/web" : `/download#${id}`}>
              <PlatformIcon platform={id} size={19} />
              {label}
            </Link>
          ))}
        </nav>
        <section className="atlas-work site-width">
          <h2>{t("离开电脑，工作继续。")}</h2>
          <WorkspaceStories locale={lang} />
        </section>
        <section className="access-host site-width">
          <div>
            <h2>{t("部署自己的连接服务。")}</h2>
            <p>
              {t(
                "用 Go 服务或 Cloudflare Worker 配对设备、交换连接信息，并在需要时转发加密流量。",
              )}
            </p>
            <Link className="site-text-link" href="/docs/self-hosting">
              {t("开源自部署指南")}
              <ArrowRight size={16} />
            </Link>
          </div>
          <div className="access-host-links">
            <Link href="/docs/self-hosting/go">
              <Server />
              <strong>{t("独立 Go 服务")}</strong>
              <small>Linux · macOS · Windows</small>
              <ArrowUpRight />
            </Link>
            <Link href="/docs/self-hosting/cloudflare">
              <Cloud />
              <strong>Cloudflare Worker</strong>
              <small>D1 · Durable Objects</small>
              <ArrowUpRight />
            </Link>
          </div>
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
