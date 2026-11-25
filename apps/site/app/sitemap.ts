import type { MetadataRoute } from "next";
import { source } from "../lib/source";
import { locales, localizeHref } from "../lib/i18n";
import { siteOrigin, languageAlternates } from "../lib/seo";
export default function sitemap(): MetadataRoute.Sitemap {
  if (!siteOrigin) return [];
  const paths = ["/", "/technology", "/pricing", "/download"];
  return locales.flatMap((locale) => [
    ...paths.map((path) => ({
      url: siteOrigin + localizeHref(path, locale),
      alternates: { languages: languageAlternates(path) },
    })),
    ...source.getPages(locale).map((page) => {
      const path = page.url.replace(/^\/zh-CN(?=\/|$)/, "");
      return {
        url: siteOrigin + page.url,
        alternates: { languages: languageAlternates(path) },
      };
    }),
  ]);
}
