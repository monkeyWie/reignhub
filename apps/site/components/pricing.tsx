"use client";
import { useT, useLocale } from "./locale";
import { LocalizedLink as Link } from "./locale";
import { useEffect, useState } from "react";
import { ArrowUpRight, Check, LoaderCircle, X } from "lucide-react";
interface Plan {
  id: string;
  name: string;
  priceLabel: string;
  purchasable: boolean;
  limits: {
    maxHostDevices: number | null;
    maxAgentsPerDevice: number | null;
    maxConcurrentConnections: number | null;
    allowRelay: boolean;
  };
}
interface PricingData {
  deployment: string;
  providers: string[];
  plans: Plan[];
}
export function PricingCards() {
  const t = useT();
  const locale = useLocale();
  const [data, setData] = useState<PricingData>();
  const [error, setError] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setError(false);
    setData(undefined);
    fetch("/api/account", { credentials: "include", signal: controller.signal })
      .then(async (r) => {
        if (!r.ok) throw Error();
        return r.json();
      })
      .then((d: PricingData) => {
        if (!Array.isArray(d.plans) || !Array.isArray(d.providers))
          throw Error();
        setData(d);
      })
      .catch(() => {
        if (!controller.signal.aborted) setError(true);
      });
    return () => controller.abort();
  }, [attempt]);
  return (
    <>
      <div className="pricing-cards" aria-live="polite">
        {!data && !error && (
          <div className="pricing-loading">
            <LoaderCircle size={20} className="spin" />
            <p>{t("正在加载方案…")}</p>
          </div>
        )}
        {error && (
          <div className="pricing-loading">
            <h2>{t("暂时无法加载官方套餐")}</h2>
            <p>{t("请稍后重试，或前往账号页查看。")}</p>
            <button
              className="button"
              onClick={() => {
                setError(false);
                setAttempt((value) => value + 1);
              }}
            >
              {t("重新加载")}
            </button>
          </div>
        )}
        {data?.deployment === "self-hosted" && (
          <div className="pricing-selfhost-info">
            <span className="site-eyebrow">SELF-HOSTED INSTANCE</span>
            <h2>{t("你正在访问自建服务。")}</h2>
            <p>
              {t(
                "这套开源部署不启用商业账号或套餐限制，可以直接连接自己的设备。",
              )}
            </p>
            <Link className="button primary" href="/web">
              {t("打开控制台")}
              <ArrowUpRight size={15} />
            </Link>
            <a
              className="site-text-link"
              href={`https://reignhub.app/${locale}/pricing`}
            >
              {t("了解官方服务 ↗")}
            </a>
          </div>
        )}
        {data?.deployment !== "self-hosted" &&
          data?.plans.map((plan) => (
            <article
              key={plan.id}
              className={
                "pricing-card " + (plan.id === "pro" ? "featured" : "")
              }
            >
              <div className="plan-heading">
                <h2>{plan.name}</h2>
                {plan.id === "pro" && (
                  <span className="site-tag">{t("更多连接空间")}</span>
                )}
              </div>
              <p className="plan-description">
                {plan.id === "free"
                  ? t("偶尔远程，先从直连开始。")
                  : t("更多设备，以及需要时的官方中继。")}
              </p>
              <div className="plan-price">
                {plan.priceLabel
                  ? locale === "en"
                    ? ((
                        { 免费: "Free", 待开放: "Coming soon" } as Record<
                          string,
                          string
                        >
                      )[plan.priceLabel] ?? plan.priceLabel)
                    : plan.priceLabel
                  : t("价格待公布")}
              </div>
              <ul>
                <li>
                  <Check size={16} />
                  {plan.limits.maxHostDevices === null
                    ? t("不限被控电脑")
                    : `${plan.limits.maxHostDevices}${t(" 台被控电脑")}`}
                </li>
                <li>
                  <Check size={16} />
                  {plan.limits.maxAgentsPerDevice === null
                    ? t("每台电脑不限 Agent 产品")
                    : `${t("每台最多 ")}${plan.limits.maxAgentsPerDevice}${t(" 种 Agent 产品")}`}
                </li>
                <li>
                  <Check size={16} />
                  {t("跨设备项目与会话管理")}
                </li>
                <li>
                  <Check size={16} />
                  {t("设备目录同步 · P2P 直连")}
                </li>
                <li>
                  <Check size={16} />
                  {t("会话与附件端到端加密")}
                </li>
                <li
                  className={!plan.limits.allowRelay ? "plan-unavailable" : ""}
                >
                  {plan.limits.allowRelay ? (
                    <Check size={16} />
                  ) : (
                    <X size={16} />
                  )}
                  {t("官方加密中继")}
                  {!plan.limits.allowRelay ? t("不包含") : ""}
                </li>
                {plan.limits.maxConcurrentConnections !== null && (
                  <li>
                    <Check size={16} />
                    {t("最多")} {plan.limits.maxConcurrentConnections}{" "}
                    {t("对并发设备连接")}
                  </li>
                )}
              </ul>
              {plan.id === "free" ? (
                data.providers.length ? (
                  <Link className="button" href="/account">
                    {t("登录开始使用")}
                    <ArrowUpRight size={15} />
                  </Link>
                ) : (
                  <button className="button" disabled>
                    {t("官方登录即将开放")}
                  </button>
                )
              ) : plan.purchasable ? (
                <Link className="button primary" href="/account">
                  {t("查看订阅")}
                  <ArrowUpRight size={15} />
                </Link>
              ) : (
                <button className="button primary" disabled>
                  {t("暂未开放购买")}
                </button>
              )}
              <small>
                {plan.id === "free"
                  ? t("手机／浏览器不占被控电脑名额。")
                  : t("生效范围与最终金额以订阅确认页为准。")}
              </small>
            </article>
          ))}
        <article className="pricing-card selfhost-plan">
          <span className="site-eyebrow">YOUR INFRASTRUCTURE</span>
          <h2>Self-hosted</h2>
          <p className="plan-description">{t("自己部署，自己决定。")}</p>
          <div className="plan-price">{t("开源免费")}</div>
          <ul>
            <li>
              <Check size={16} />
              {t("不设商业设备额度")}
            </li>
            <li>
              <Check size={16} />
              {t("不设商业 Agent 产品额度")}
            </li>
            <li>
              <Check size={16} />
              {t("自己的 Web、信令与加密中继")}
            </li>
            <li>
              <Check size={16} />
              {t("无需账号与订阅")}
            </li>
            <li>
              <Check size={16} />
              {t("可选自建 TCP／UDP 探测")}
            </li>
            <li>
              <Check size={16} />
              {t("MIT 许可，核心源码可修改")}
            </li>
          </ul>
          <Link className="button" href="/docs/self-hosting">
            {t("开始部署")}
            <ArrowUpRight size={15} />
          </Link>
          <small>{t("Cloudflare 等基础设施费用由你承担。")}</small>
        </article>
      </div>
      {data?.deployment !== "self-hosted" && data?.plans.length === 0 && (
        <p className="pricing-status">
          {t("此服务尚未公布可用套餐，请以账号页的开放状态为准。")}
        </p>
      )}
      <p className="pricing-status">
        {t(
          "ReignHub 服务方案与 Agent 模型费用独立，模型订阅或 API 用量由你使用的模型服务计费。",
        )}
      </p>
    </>
  );
}
