import { PlatformIcon } from "./platform-icon";
import { ArrowUpRight, Code2, Download } from "lucide-react";
import { translate, type Locale } from "../lib/i18n";
import {
  releasesURL,
  formatAssetSize,
  type Platform,
  type ReleaseCatalog,
} from "../lib/releases";
import { LocalizedLink as Link } from "./locale";

export function ReleaseDownloads({
  lang,
  catalog,
}: {
  lang: Locale;
  catalog: ReleaseCatalog;
}) {
  const t = translate(lang);
  const release = catalog.release;
  const platforms = [
    {
      id: "macos",
      title: "macOS",
      arch: "Apple Silicon / Intel",
      format: "DMG",
      note: t(
        "打开 DMG，将 ReignHub 拖入 Applications。当前预览尚未完成 Apple 公证。",
      ),
    },
    {
      id: "windows",
      title: "Windows",
      arch: "x64 / arm64",
      format: t("EXE 安装程序"),
      note: t("提供 x64 与 arm64 安装程序，包含快捷方式和卸载功能。"),
    },
    {
      id: "linux",
      title: "Linux",
      arch: "x64 / arm64",
      format: "DEB / Arch / AppImage",
      note: t("选择适合发行版的安装包，或使用 AppImage。运行需要 GTK 4。"),
    },
    {
      id: "android",
      title: "Android",
      arch: "arm64",
      format: "APK",
      note: t("下载并安装 APK，随时连接你的 Agent。"),
    },
    {
      id: "ios",
      title: "iOS",
      arch: "iPhone / iPad",
      format: t("未签名 IPA"),
      note: t("适用于 iPhone 与 iPad，真机安装需使用自己的 Apple 开发签名。"),
    },
  ] satisfies {
    id: Platform;
    title: string;
    arch: string;
    format: string;
    note: string;
  }[];
  return (
    <>
      <section className="site-width release-notice">
        <span className="site-tag">
          {release
            ? release.prerelease
              ? t("开发预览")
              : t("最新版本")
            : "REIGNHUB"}
        </span>
        <div>
          <h2>
            {release ? `ReignHub ${release.version}` : t("获取 ReignHub")}
          </h2>
          <p>
            {release
              ? `${t("发布于")} ${new Intl.DateTimeFormat(lang, { dateStyle: "medium", timeZone: "UTC" }).format(new Date(release.publishedAt))}`
              : catalog.checkedAt
                ? t("安装包发布后会自动显示在这里。")
                : t("暂时无法获取版本，请在 GitHub Releases 查看下载。")}
          </p>
          <div className="site-actions">
            <a href={release?.url || releasesURL} className="site-text-link">
              {t("查看 Releases ↗")}
            </a>
            {release?.checksumsURL && (
              <a href={release.checksumsURL} className="site-text-link">
                SHA256SUMS <ArrowUpRight size={14} />
              </a>
            )}
          </div>
        </div>
      </section>
      <section className="site-section site-width">
        <div className="download-grid">
          {platforms.map(({ id, title, arch, format, note }) => {
            const assets =
              release?.assets
                .filter((a) => a.platform === id)
                .sort(
                  (a, b) =>
                    a.architecture.localeCompare(b.architecture) ||
                    a.name.localeCompare(b.name),
                ) || [];
            return (
              <article key={id} id={id}>
                <PlatformIcon platform={id} size={25} />
                <h2>{title}</h2>
                <p className="download-arch">{arch}</p>
                <span className="site-tag">{format}</span>
                <p>{note}</p>
                <div className="download-assets">
                  {assets.map((asset) => (
                    <a
                      key={asset.name}
                      href={asset.url}
                      className="button"
                      title={asset.name}
                    >
                      <Download size={14} />
                      {id === "macos"
                        ? asset.architecture === "arm64"
                          ? "Apple Silicon"
                          : "Intel"
                        : id === "ios"
                          ? "IPA"
                          : asset.architecture}
                      {id === "linux" && (
                        <span>
                          {asset.format === "arch"
                            ? "Arch"
                            : asset.format === "appimage"
                              ? "AppImage"
                              : "DEB"}
                        </span>
                      )}
                      <span
                        className="download-size"
                        title={`${asset.size.toLocaleString(lang)} bytes`}
                      >
                        {formatAssetSize(asset.size)}
                      </span>
                    </a>
                  ))}
                  {!assets.length && (
                    <span className="download-unavailable">
                      {t("安装包即将发布")}
                    </span>
                  )}
                </div>
              </article>
            );
          })}
          <article className="download-source">
            <Code2 size={25} />
            <h2>{t("从源码构建")}</h2>
            <p className="download-arch">Go + NSGo · Next.js</p>
            <span className="site-tag">MIT</span>
            <p>
              {t(
                "使用自己的默认服务地址，或为目标平台构建原生客户端。版本与工具链以仓库为准。",
              )}
            </p>
            <Link
              className="site-text-link"
              href="/docs/contributing/development"
            >
              {t("本地开发指南")}
              <ArrowUpRight size={14} />
            </Link>
          </article>
        </div>
      </section>
      <section
        id="server"
        className="site-section site-width hosting-downloads"
      >
        <h2>{t("服务端下载")}</h2>
        <p>{t("Go 服务按系统和 CPU 架构打包，内含程序和 config.json。")}</p>
        <div className="download-assets">
          {release?.hostingAssets?.map((asset) => (
            <a
              className="button"
              key={asset.name}
              href={asset.url}
              title={asset.name}
            >
              <Download size={14} />
              {asset.label}
            </a>
          ))}
        </div>
        <div className="site-actions">
          <Link className="site-text-link" href="/docs/self-hosting">
            {t("安装与配置")} <ArrowUpRight size={14} />
          </Link>
          <a className="site-text-link" href={release?.url || releasesURL}>
            {t("查看发布附件")} <ArrowUpRight size={14} />
          </a>
        </div>
      </section>
    </>
  );
}
