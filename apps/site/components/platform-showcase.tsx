import { PlatformIcon, type PlatformIconName } from "./platform-icon";
import { ArrowUpRight } from "lucide-react";
import { translate, type Locale } from "../lib/i18n";
import { LocalizedLink as Link } from "./locale";

export function PlatformShowcase({ lang }: { lang: Locale }) {
  const t = translate(lang);
  return (
    <section className="platform-section site-width">
      <div className="platform-heading">
        <div>
          <span className="site-eyebrow">SIX PLATFORMS. ONE WORKSPACE.</span>
          <h2>{t("每一块屏幕，都是你的主场。")}</h2>
        </div>
        <p>{t("桌面原生，移动随行，浏览器即开即用。")}</p>
      </div>
      <div className="platform-grid">
        {[
          ["macos", "macOS", t("原生桌面")],
          ["windows", "Windows", t("原生桌面")],
          ["linux", "Linux", t("原生桌面")],
          ["ios", "iOS", t("移动掌控")],
          ["android", "Android", t("移动掌控")],
          ["web", "Web", t("打开即连接")],
        ].map(([platform, name, caption]) => {
          return (
            <Link
              href={name === "Web" ? "/web" : "/download"}
              className="platform-card"
              key={String(name)}
            >
              <PlatformIcon platform={platform as PlatformIconName} size={25} />
              <strong>{String(name)}</strong>
              <span>{String(caption)}</span>
              <ArrowUpRight className="platform-arrow" size={14} />
            </Link>
          );
        })}
      </div>
    </section>
  );
}
