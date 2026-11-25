import { pageMetadata, marketingMeta } from "../../../lib/seo";
import { translate, isLocale } from "../../../lib/i18n";
import { notFound } from "next/navigation";
import { LocalizedLink as Link } from "../../../components/locale";
import {
  ArrowRight,
  FileImage,
  Layers3,
  Terminal,
  Code2,
  FolderOpen,
  KeyRound,
  MessagesSquare,
  MonitorSmartphone,
  Network,
  ShieldCheck,
  WifiOff,
} from "lucide-react";
import {
  BottomCTA,
  PageIntro,
  SectionHeading,
  SiteShell,
} from "../../../components/site";
import { PlatformShowcase } from "../../../components/platform-showcase";
import { WorkspaceStories } from "../../../components/workspace-stories";
import { WorkspaceDemo } from "../../../components/workspace-demo";
import "../home.css";
export default async function Features({
  params,
}: {
  params: Promise<{ lang: string }>;
}) {
  const { lang } = await params;
  if (!isLocale(lang)) notFound();
  const t = translate(lang);
  return (
    <SiteShell>
      <main id="main">
        <PageIntro
          eyebrow="THE WORKSPACE, EVERYWHERE"
          title={
            <>
              {t("熟悉的 Agent，")}
              <br />
              <span>{t("更自由的工作方式。")}</span>
            </>
          }
        >
          {t("把电脑上的工作带到手边，保留原来的 Agent、模型与项目。")}
        </PageIntro>
        <div className="features-showcase site-width">
          <WorkspaceDemo locale={lang} />
          <div>
            <span className="site-tag available">{t("桌面 · 手机 · Web")}</span>
            <h2>
              {t("一台电脑工作，")}
              <br />
              {t("不止一台设备参与。")}
            </h2>
            <p>
              {t(
                "桌面既是被控端，也是控制端。手机和浏览器连接你的电脑；多台电脑，各自的项目和会话都能在一个界面找到。",
              )}
            </p>
            <Link className="site-text-link" href="/docs/guides/devices">
              {t("设备与配对")}
              <ArrowRight size={14} />
            </Link>
          </div>
        </div>
        <section className="site-width">
          <WorkspaceStories locale={lang} />
        </section>
        <PlatformShowcase lang={lang} />
        <section className="site-section site-width">
          <div className="feature-detail-grid">
            {[
              [
                FolderOpen,
                t("以项目为中心"),
                t(
                  "多台设备、多个 Agent 的会话，按工作目录聚合、最近更新排序。打开项目，立刻找回工作上下文。",
                ),
                "/docs/guides/sessions",
              ],
              [
                MessagesSquare,
                t("对话顺着思路发生"),
                t(
                  "增量输出、Markdown、生成中的折叠活动、模型选择，以及可跨段落选择和复制的消息。",
                ),
                "/docs/guides/sessions",
              ],
              [
                FileImage,
                t("图片与文件一起带上"),
                t(
                  "选择、拖放或在支持的环境粘贴附件；图片缩略图、放大切换和文件下载，附件加密传输。",
                ),
                "/docs/guides/files",
              ],
              [
                Network,
                t("能直连，就直连"),
                t(
                  "原生端探索 UDP 与 TCP 直连，浏览器使用标准 WebRTC。自动选择可用路径，加密保护每一次传输。",
                ),
                "/docs/guides/connections",
              ],
              [
                KeyRound,
                t("每次连接，有明确的信任"),
                t(
                  "设备编号负责发现，首次访问由电脑确认或密码验证；已授权设备可以撤销。",
                ),
                "/docs/guides/devices",
              ],
              [
                WifiOff,
                t("断线有缓存，重连有同步"),
                t(
                  "SQLite 与 IndexedDB 保存已同步内容。恢复连接后补最新快照，再继续增量订阅。",
                ),
                "/docs/guides/sessions",
              ],
              [
                ShieldCheck,
                t("数据的边界，清清楚楚"),
                t(
                  "会话和附件端到端加密，密钥保存在设备上；云端只保存连接所需的元数据和可选账号信息。",
                ),
                "/docs/privacy",
              ],
              [
                MonitorSmartphone,
                t("用自己的服务也一样"),
                t(
                  "自建 Worker 同时提供 Web、信令与加密中继，无商业设备或 Agent 配额。",
                ),
                "/docs/self-hosting",
              ],
            ].map(([Icon, title, description, url]) => {
              const I = Icon as typeof FolderOpen;
              return (
                <article key={String(title)}>
                  <I size={22} />
                  <h2>{String(title)}</h2>
                  <p>{String(description)}</p>
                  <Link href={String(url)} className="site-text-link">
                    {t("阅读文档")}
                    <ArrowRight size={14} />
                  </Link>
                </article>
              );
            })}
          </div>
        </section>
        <section className="site-width support-section">
          <SectionHeading
            eyebrow="YOUR AGENTS. CONNECTED."
            title={t("为真实的 Agent 工作流而生。")}
          >
            {t(
              "保留你的模型账号、项目环境与会话，让远程操作与桌面工作连成一体。",
            )}
          </SectionHeading>
          <div className="support-grid">
            {[
              [
                Terminal,
                "Codex",
                t("桌面与远端，共享同一段对话"),
                "/docs/agents/codex",
              ],
              [
                Layers3,
                "Claude Code · ACP",
                t("成熟 Agent，统一远程体验"),
                "/docs/agents/acp",
              ],
              [
                Code2,
                "Pi",
                t("原生 RPC，实时消息与工具审批"),
                "/docs/agents/pi",
              ],
              [
                Layers3,
                t("开放 Provider 架构"),
                t("统一接入、会话与连接能力，为扩展更多 Agent 工作流而设计。"),
                "/docs/contributing/providers",
              ],
            ].map(([Icon, title, description, href]) => {
              const I = Icon as typeof Terminal;
              return (
                <div key={String(title)}>
                  <I size={22} />
                  <strong>{String(title)}</strong>
                  <p>{String(description)}</p>
                  <Link className="site-text-link" href={String(href)}>
                    {t("了解更多")}
                    <ArrowRight size={14} />
                  </Link>
                </div>
              );
            })}
          </div>
        </section>
        <BottomCTA />
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
  const [title, description] = marketingMeta["/features"][lang];
  return pageMetadata(lang, "/features", title, description);
}
