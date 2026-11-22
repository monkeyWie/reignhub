// Only metadata crosses the Worker: binaries download directly from GitHub.
export const releaseRepository = "monkeyWie/reignhub";
export const releasesURL = `https://github.com/${releaseRepository}/releases`;
export type Platform = "macos" | "windows" | "linux" | "android" | "ios";
export interface ReleaseAsset {
  name: string;
  url: string;
  size: number;
  platform: Platform;
  architecture: string;
  format: "dmg" | "exe" | "deb" | "arch" | "appimage" | "apk" | "ipa";
}
export interface HostingAsset {
  name: string;
  url: string;
  label: string;
}
export interface ReleaseVersion {
  version: string;
  url: string;
  publishedAt: string;
  prerelease: boolean;
  assets: ReleaseAsset[];
  hostingAssets: HostingAsset[];
  checksumsURL: string | null;
}
export interface ReleaseCatalog {
  release: ReleaseVersion | null;
  checkedAt: string | null;
  stale: boolean;
}

// GitHub reports exact bytes. Use binary units explicitly, matching the
// numerical sizes users see on Releases rather than decimal MB.
export function formatAssetSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let value = bytes / 1024;
  let index = 0;
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024;
    index++;
  }
  return `${value.toFixed(2)} ${units[index]}`;
}

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}
// Reject arbitrary redirect destinations supplied by malformed upstream data.
function assetURL(value: unknown, tag: string): string | null {
  if (typeof value !== "string") return null;
  try {
    const url = new URL(value);
    return url.origin === "https://github.com" &&
      !url.search &&
      !url.hash &&
      url.pathname.startsWith(
        `/${releaseRepository}/releases/download/${encodeURIComponent(tag)}/`,
      )
      ? url.href
      : null;
  } catch {
    return null;
  }
}
export function parseReleases(value: unknown): ReleaseVersion | null {
  if (!Array.isArray(value)) throw new Error("Invalid GitHub release response");
  const candidates = value
    .map(record)
    .filter(
      (r): r is Record<string, unknown> =>
        !!r &&
        r.draft === false &&
        typeof r.tag_name === "string" &&
        /^v\d+\.\d+\.\d+(?:-[\w.-]+)?$/.test(r.tag_name) &&
        typeof r.published_at === "string" &&
        Number.isFinite(Date.parse(r.published_at)) &&
        Array.isArray(r.assets),
    )
    .sort(
      (a, b) =>
        Date.parse(b.published_at as string) -
        Date.parse(a.published_at as string),
    );
  for (const release of candidates) {
    const tag = release.tag_name as string;
    const version = tag.slice(1);
    const assets: ReleaseAsset[] = [];
    const hostingAssets: HostingAsset[] = [];
    let checksumsURL: string | null = null;
    for (const item of release.assets as unknown[]) {
      const a = record(item);
      if (!a || typeof a.name !== "string") continue;
      const url = assetURL(a.browser_download_url, tag);
      if (!url) continue;
      if (a.name === "SHA256SUMS") {
        checksumsURL = url;
        continue;
      }
      if (typeof a.size !== "number" || a.size <= 0) continue;
      const serverPrefix = `reignhub-server-${version}-`;
      if (a.name.startsWith(serverPrefix)) {
        const server =
          /^(linux|darwin|windows)-(amd64|arm64)(\.tar\.gz|\.zip)$/.exec(
            a.name.slice(serverPrefix.length),
          );
        if (server && (server[1] === "windows") === (server[3] === ".zip")) {
          const os = { darwin: "macOS", linux: "Linux", windows: "Windows" }[
            server[1]
          ];
          hostingAssets.push({
            name: a.name,
            url,
            label: `${os} · ${server[2]}`,
          });
        }
        continue;
      }
      const prefix = `reignhub-${version}-`;
      if (!a.name.startsWith(prefix)) continue;
      const suffix = a.name.slice(prefix.length);
      const match =
        /^(macos|windows|linux)-(arm64|x64)(\.dmg|-setup\.exe|\.deb|\.pkg\.tar\.gz|\.AppImage)$/.exec(
          suffix,
        );
      let platform: Platform, architecture: string;
      let format: ReleaseAsset["format"];
      if (match) {
        platform = match[1] as Platform;
        const extensions: Record<string, ReleaseAsset["format"]> = {
          ".dmg": "dmg",
          "-setup.exe": "exe",
          ".deb": "deb",
          ".pkg.tar.gz": "arch",
          ".AppImage": "appimage",
        };
        format = extensions[match[3]];
        if (
          (platform === "macos" && format !== "dmg") ||
          (platform === "windows" && format !== "exe") ||
          (platform === "linux" &&
            !["deb", "arch", "appimage"].includes(format))
        )
          continue;
        architecture = match[2];
      } else if (suffix === "android-arm64.apk") {
        platform = "android";
        architecture = "arm64";
        format = "apk";
      } else if (suffix === "ios-unsigned.ipa") {
        platform = "ios";
        architecture = "universal";
        format = "ipa";
      } else {
        continue;
      }
      assets.push({
        name: a.name,
        url,
        size: a.size,
        platform,
        architecture,
        format,
      });
    }
    // Ignore incomplete/draft-only releases rather than advertising empty downloads.
    if (!assets.length) continue;
    return {
      version,
      url: `${releasesURL}/tag/${encodeURIComponent(tag)}`,
      publishedAt: release.published_at as string,
      prerelease: release.prerelease === true,
      assets,
      hostingAssets,
      checksumsURL,
    };
  }
  return null;
}
