import { ArrowUpRight, LockKeyhole, Monitor, Smartphone } from "lucide-react";
import { LocalizedLink as Link } from "./locale";
import { type Locale, translate } from "../lib/i18n";
import { ThemedShot } from "./site-theme";
import "./workspace-stories.css";

export function WorkspaceStories({ locale }: { locale: Locale }) {
  const t = translate(locale);
  const stories = [
    {
      title: t("一个项目，多个 Agent。"),
      body: t(
        "按项目聚合 Codex、Claude Code 和 Pi 的会话。在最近列表里找回上次的工作，绿点会标出刚完成的回复。",
      ),
      href: "/docs/guides/sessions",
      label: t("查看会话指南"),
      visual: (
        <div className="story-session">
          <ThemedShot
            base={`/workspace/mac-${locale}-`}
            loading="lazy"
            width={2160}
            height={1620}
            alt={t("按项目和最近活动排列的会话列表")}
          />
        </div>
      ),
    },
    {
      title: t("扫一下，连接工作电脑。"),
      body: t(
        "在电脑上打开配对码，在手机上扫码或输入六位设备编号。确认授权后，就能远程查看进度、发送指令。",
      ),
      href: "/docs/guides/devices",
      label: t("设备与配对"),
      visual: (
        <div className="story-pair">
          <img
            src="/workspace/pairing.webp"
            width="128"
            height="128"
            loading="lazy"
            alt={t("示例设备二维码")}
          />
          <div>
            <small>{t("设备编号")}</small>
            <strong>266 556</strong>
          </div>
        </div>
      ),
    },
    {
      title: t("P2P 直连，端到端加密。"),
      body: t(
        "设备优先直连。需要中继时，会话内容和附件仍然只有配对设备能读取。",
      ),
      href: "/docs/guides/connections",
      label: t("了解设备连接"),
      visual: (
        <div className="story-network" aria-hidden="true">
          <Monitor />
          <i />
          <LockKeyhole />
          <i />
          <Smartphone />
        </div>
      ),
    },
    {
      title: t("远程终端"),
      body: t("在项目目录打开 Shell。换一台设备，也能接着看命令输出。"),
      href: "/docs/guides/terminal",
      label: t("打开终端指南"),
      visual: (
        <div className="story-term-shot">
          <ThemedShot
            base="/workspace/terminal-"
            loading="lazy"
            width={1200}
            height={505}
            alt={t("远程终端中显示项目测试的命令和输出")}
          />
        </div>
      ),
    },
  ];
  return (
    <div className="workspace-stories">
      {stories.map((s) => (
        <article className="workspace-story" key={s.href}>
          {s.visual}
          <div className="story-copy">
            <h3>{s.title}</h3>
            <p>{s.body}</p>
            <Link href={s.href}>
              {s.label}
              <ArrowUpRight size={15} />
            </Link>
          </div>
        </article>
      ))}
      <article className="workspace-story">
        <div className="story-deploy">
          <a href="https://deploy.workers.cloudflare.com/?url=https://github.com/monkeyWie/reignhub">
            <img
              src="/workspace/deploy-cloudflare.svg"
              width="184"
              height="39"
              alt="Deploy to Cloudflare"
            />
          </a>
        </div>
        <div className="story-copy">
          <h3>{t("部署自己的连接服务。")}</h3>
          <p>
            {t("用自己的 Cloudflare 账号一键部署，配对设备并运行 Web 控制台。")}
          </p>
          <Link href="/docs/self-hosting/cloudflare">
            {t("Cloudflare 部署指南")}
            <ArrowUpRight size={15} />
          </Link>
          <Link className="secondary-guide" href="/docs/self-hosting/go">
            {t("独立 Go 服务")}
          </Link>
        </div>
      </article>
    </div>
  );
}
