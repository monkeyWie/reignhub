import { pageMetadata, marketingMeta } from "../../../lib/seo";
import { translate, isLocale } from "../../../lib/i18n";
import { notFound } from "next/navigation";
import { LocalizedLink as Link } from "../../../components/locale";
import { ArrowUpRight, Globe } from "lucide-react";
import { ReleaseDownloads } from "../../../components/release-downloads";
import { PageIntro, SiteShell } from "../../../components/site";
import { getServerReleaseCatalog } from "../../../lib/release-server";

export default async function DownloadPage({
  params,
}: {
  params: Promise<{ lang: string }>;
}) {
  const { lang } = await params;
  if (!isLocale(lang)) notFound();
  const t = translate(lang);
  const catalog = await getServerReleaseCatalog();
  return (
    <SiteShell>
      <main id="main">
        <PageIntro
          eyebrow="NATIVE WHEN YOU WANT IT"
          title={
            <>
              {t("选一个屏幕，")}
              <br />
              <span>{t("带走你的工作。")}</span>
            </>
          }
        >
          {t(
            "在桌面上聚合本地和已配对电脑的 Agent 会话，用手机或浏览器继续同一个项目。",
          )}
        </PageIntro>
        <section className="site-width download-web">
          <div>
            <Globe size={28} />
            <h2>{t("也可以，什么都不安装。")}</h2>
            <p>
              {t(
                "浏览器打开控制台，输入设备编号，配对后继续对话。被控电脑仍需运行 ReignHub 桌面端。",
              )}
            </p>
          </div>
          <Link className="button primary" href="/web">
            {t("打开 Web 控制台")}
            <ArrowUpRight size={16} />
          </Link>
        </section>
        <ReleaseDownloads lang={lang} catalog={catalog} />
        <section className="site-width download-help">
          <h2>{t("第一次使用？")}</h2>
          <p>
            {t(
              "先在安装 Agent 的电脑上启动 ReignHub，按面板提示接入 Codex。然后在另一台设备输入编号，完成授权。",
            )}
          </p>
          <Link className="button" href="/docs/quick-start">
            {t("查看快速开始")}
            <ArrowUpRight size={15} />
          </Link>
          <p className="measurement-note">
            {t("安装包大小见上方各平台链接。macOS 预览构建的内存测量见")}{" "}
            <Link href="/docs/architecture/performance">{t("性能说明")}</Link>。
          </p>
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
  const [title, description] = marketingMeta["/download"][lang];
  return pageMetadata(lang, "/download", title, description);
}
