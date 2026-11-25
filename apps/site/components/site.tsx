"use client";
import { localizeHref } from "../lib/i18n";
import { legalLinks } from "../lib/legal";
import { useLocale, useT } from "./locale";
import { LocalizedLink as Link } from "./locale";
import "./site.css";
import { ArrowUpRight } from "lucide-react";
import type { ReactNode } from "react";
import { SiteHeader } from "./site-header";

export function SiteShell({ children }: { children: ReactNode }) {
  const t = useT();
  return (
    <div className="site-shell">
      <a className="site-skip" href="#main">
        {t("跳到主要内容")}
      </a>
      <SiteHeader />
      {children}
      <SiteFooter />
    </div>
  );
}
export function SiteFooter() {
  const t = useT();
  const locale = useLocale();
  const legal = legalLinks(locale);
  return (
    <footer className="site-footer site-width">
      <div className="footer-top">
        <div>
          <Link className="brand" href="/">
            ♜ ReignHub
          </Link>
          <p>{t("你的 Agent 聚合工作台。")}</p>
        </div>
        {[
          [
            t("产品"),
            [
              [t("特性"), localizeHref("/", locale) + "#features"],
              [t("技术栈"), "/technology"],
              [t("价格"), "/pricing"],
              [t("下载"), "/download"],
            ],
          ],
          [
            t("文档"),
            [
              [t("快速开始"), "/docs/quick-start"],
              [t("Agent 接入"), "/docs/agents"],
              [t("自建服务"), "/docs/self-hosting"],
              [t("常见问题"), "/docs/guides/troubleshooting"],
            ],
          ],
          [
            t("开发者"),
            [
              ["GitHub", "https://github.com/monkeyWie/reignhub"],
              [t("贡献指南"), "/docs/contributing"],
              [t("连接协议"), "/docs/architecture/protocol"],
              [t("安全与隐私"), "/docs/privacy"],
            ],
          ],
        ].map(([title, items]) => (
          <div className="footer-links" key={String(title)}>
            <strong>{String(title)}</strong>
            {(items as string[][]).map(([label, href]) => (
              <Link key={href} href={href}>
                {label}
              </Link>
            ))}
          </div>
        ))}
      </div>
      <div className="footer-bottom">
        <span>
          © {new Date().getFullYear()}
          {t("ReignHub · 开源核心采用 MIT 许可")}
        </span>
        {legal ? (
          <nav className="footer-legal" aria-label={t("法律信息")}>
            <a href={legal.terms}>{t("服务条款")}</a>
            <a href={legal.privacy}>{t("隐私政策")}</a>
          </nav>
        ) : (
          <span>{t("原生客户端 · Web 控制台 · 可自行部署")}</span>
        )}
      </div>
    </footer>
  );
}
export function PageIntro({
  eyebrow,
  title,
  children,
}: {
  eyebrow: string;
  title: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="page-intro site-width">
      <span className="site-eyebrow">{eyebrow}</span>
      <h1>{title}</h1>
      <p>{children}</p>
    </section>
  );
}
export function SectionHeading({
  eyebrow,
  title,
  children,
}: {
  eyebrow: string;
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className="section-heading">
      <span className="site-eyebrow">{eyebrow}</span>
      <h2>{title}</h2>
      {children && <p>{children}</p>}
    </div>
  );
}
export function BottomCTA() {
  const t = useT();
  return (
    <section className="site-cta site-width">
      <div>
        <span className="site-eyebrow">TAKE YOUR WORK WITH YOU</span>
        <h2>{t("你的下一步，不受距离限制。")}</h2>
        <p>{t("六大平台，原生轻量。现在，把你的 Agent 带到身边。")}</p>
      </div>
      <Link className="button primary" href="/docs/quick-start">
        {t("开始使用")}
        <ArrowUpRight size={16} />
      </Link>
    </section>
  );
}
