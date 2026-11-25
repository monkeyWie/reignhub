"use client";
import { useEffect, useState } from "react";
import { Download, ArrowUpRight } from "lucide-react";
import { LocalizedLink as Link, useT } from "./locale";
import {
  formatAssetSize,
  type ReleaseAsset,
  type ReleaseCatalog,
  type Platform,
} from "../lib/releases";

const names = {
  macos: "macOS",
  windows: "Windows",
  linux: "Linux",
  ios: "iOS",
  android: "Android",
};
async function preferredAsset(assets: ReleaseAsset[]) {
  const ua = navigator.userAgent;
  let platform: Platform | undefined;
  if (
    /iPad|iPhone/.test(ua) ||
    (/Mac/.test(ua) && navigator.maxTouchPoints > 1)
  )
    platform = "ios";
  else if (/Android/.test(ua)) platform = "android";
  else if (/Mac/.test(ua)) platform = "macos";
  else if (/Windows/.test(ua)) platform = "windows";
  else if (/Linux/.test(ua)) platform = "linux";
  if (!platform) return;
  const hints = (
    navigator as Navigator & {
      userAgentData?: {
        getHighEntropyValues(
          keys: string[],
        ): Promise<{ architecture?: string }>;
      };
    }
  ).userAgentData;
  const cpu = (
    await hints?.getHighEntropyValues(["architecture"]).catch(() => undefined)
  )?.architecture;
  const arm = cpu
    ? /arm/i.test(cpu)
    : /arm64|aarch64/i.test(ua) || platform === "macos";
  const choices = assets.filter((a) => a.platform === platform);
  if (platform === "ios" || platform === "android") return choices[0];
  return choices.find(
    (a) =>
      a.architecture === (arm ? "arm64" : "x64") &&
      (platform !== "linux" || a.format === "appimage"),
  );
}
export function HeroDownload() {
  const t = useT();
  const [asset, setAsset] = useState<ReleaseAsset>();
  useEffect(() => {
    const controller = new AbortController();
    async function resolve() {
      const response = await fetch("/api/releases", {
        signal: controller.signal,
      });
      if (!response.ok) return;
      const catalog: ReleaseCatalog = await response.json();
      const choice = await preferredAsset(catalog.release?.assets ?? []);
      if (!controller.signal.aborted) setAsset(choice);
    }
    void resolve().catch(() => {});
    return () => controller.abort();
  }, []);
  return (
    <div className="site-actions hero-actions">
      {asset && (
        <a href={asset.url} className="button primary hero-download">
          <Download size={16} />
          {t("下载 {platform} 版").replace("{platform}", names[asset.platform])}{" "}
          <small>
            {asset.format === "ipa" ? t("未签名 IPA") : asset.architecture} ·{" "}
            {formatAssetSize(asset.size)}
          </small>
        </a>
      )}
      <Link href="/download" className="atlas-inline">
        {t("更多版本")}
        <ArrowUpRight size={15} />
      </Link>
    </div>
  );
}
