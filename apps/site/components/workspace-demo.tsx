import { Laptop, Smartphone } from "lucide-react";
import { translate, type Locale } from "../lib/i18n";

export function WorkspaceDemo({ locale }: { locale: Locale }) {
  const t = translate(locale);
  return (
    <figure className="workspace-photos">
      {(["mac", "phone"] as const).map((device) => {
        const base = `/workspace/${device}-${locale}-`;
        const phone = device === "phone";
        return (
          <div className={`workspace-shot shot-${device}`} key={device}>
            <span className="shot-label">
              {phone ? <Smartphone size={14} /> : <Laptop size={15} />}
              {phone ? t("iPhone · 远程追问") : t("Mac · Codex 会话")}
            </span>
            <picture>
              <source
                media="(prefers-color-scheme: dark)"
                srcSet={base + "dark.webp"}
              />
              <img
                src={base + "light.webp"}
                width={phone ? 1206 : 2020}
                height={phone ? 2622 : 1220}
                alt={
                  phone
                    ? t(
                        "在 iPhone 的 ReignHub 会话中追问空结果测试，收到电脑上的 Codex 回复。",
                      )
                    : t(
                        "Mac 版 ReignHub 正显示同一段 Codex 会话和刚完成的测试结果。",
                      )
                }
              />
            </picture>
          </div>
        );
      })}
    </figure>
  );
}
