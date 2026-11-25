import { ArrowUpRight, LockKeyhole, Monitor, Smartphone } from "lucide-react";
import { LocalizedLink as Link } from "./locale";
import { type Locale, translate } from "../lib/i18n";
import "./workspace-stories.css";

export function WorkspaceStories({ locale }: { locale: Locale }) {
  const t = translate(locale);
  return (
    <div className="workspace-stories">
      <article className="workspace-story">
        <div className="story-terminal" aria-hidden="true">
          <div>
            <span>atlas</span>
            <span>zsh</span>
          </div>
          <pre>
            <b>~/atlas</b> $ pnpm test{"\n\n"}✓ search.test.tsx{"\n"}✓
            auth.test.tsx{"\n\n"}Tests　12 passed
          </pre>
        </div>
        <div className="story-copy">
          <h3>{t("远程终端")}</h3>
          <p>{t("在项目目录打开 Shell。换一台设备，也能接着看命令输出。")}</p>
          <Link href="/docs/guides/terminal">
            {t("打开终端指南")} <ArrowUpRight size={15} />
          </Link>
        </div>
      </article>
      <article className="workspace-story">
        <div className="story-network" aria-hidden="true">
          <Monitor />
          <i />
          <LockKeyhole />
          <i />
          <Smartphone />
        </div>
        <div className="story-copy">
          <h3>{t("设备之间，端到端加密。")}</h3>
          <p>
            {t(
              "设备优先直连。需要中继时，会话内容和附件仍然只有配对设备能读取。",
            )}
          </p>
          <Link href="/docs/guides/connections">
            {t("了解设备连接")} <ArrowUpRight size={15} />
          </Link>
        </div>
      </article>
    </div>
  );
}
