import { translate, type Locale } from "../lib/i18n";
import { ThemedShot } from "./site-theme";
export function WorkspaceDemo({ locale }: { locale: Locale }) {
  const t = translate(locale);
  return (
    <figure className="workspace-photos">
      <div className="workspace-shot shot-mac">
        <ThemedShot
          base={`/workspace/mac-${locale}-`}
          width={2160}
          height={1620}
          alt={t("Mac 版 ReignHub 正显示同一段 Codex 会话和刚完成的测试结果。")}
        />
      </div>
      <div className="workspace-shot shot-phone">
        <ThemedShot
          base={`/workspace/phone-${locale}-`}
          width={1206}
          height={2472}
          alt={t(
            "在 iPhone 的 ReignHub 会话中追问空结果测试，收到电脑上的 Codex 回复。",
          )}
        />
      </div>
    </figure>
  );
}
