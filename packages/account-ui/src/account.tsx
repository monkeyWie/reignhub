"use client";
import { useEffect, useState } from "react";
import { Button } from "./ui";
import { EmailSignIn } from "./email-sign-in";
interface Limits {
  maxHostDevices: number | null;
  maxAgentsPerDevice: number | null;
  maxConcurrentConnections: number | null;
  allowRelay: boolean;
}
interface Account {
  user: { id: string; name: string; email: string };
  plan: string;
  limits: Limits;
  admin: boolean;
  canSetPassword?: boolean;
  controllers?: { id: string; name: string }[];
  devices: {
    id: string;
    name: string;
    connectionId: string;
  }[];
}
interface Plan {
  id: string;
  name: string;
  limits: Limits;
  priceLabel: string;
  purchasable: boolean;
}
async function api<T>(path: string, body?: unknown): Promise<T> {
  const r = await fetch(path, {
    credentials: "include",
    ...(body === undefined
      ? {}
      : {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        }),
  });
  const data = (await r.json()) as { message?: string };
  if (!r.ok) throw Error(data.message || "请求失败");
  return data as T;
}
export function AccountPage({
  legal,
}: { legal?: { terms: string; privacy: string } } = {}) {
  const [account, setAccount] = useState<Account>();
  const [plans, setPlans] = useState<Plan[]>([]);
  const [providers, setProviders] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [newPassword, setNewPassword] = useState("");
  async function refresh() {
    try {
      const data = await api<{
        account: Account | null;
        providers: string[];
        plans: Plan[];
        deployment: string;
      }>("/api/account");
      setAccount(data.account || undefined);
      setPlans(data.plans);
      setProviders(data.providers);
      const url = new URL(location.href);
      const provider = url.searchParams.get("provider");
      if (
        url.searchParams.has("code") &&
        (provider === "github" || provider === "google")
      ) {
        url.searchParams.delete("provider");
        history.replaceState(history.state, "", url);
        if (data.providers.includes(provider)) await login(provider);
        else setError("此服务尚未启用该登录方式");
      }
      if (data.deployment === "self-hosted")
        setError(
          "这是自行部署的开源服务，无需账号，连接与中继没有商业套餐限制。",
        );
    } catch {
      setError("此服务没有启用账号系统。开源自建服务可直接连接设备。");
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    void refresh();
    const p = new URLSearchParams(location.search);
    if (p.get("code")) {
      setCode(p.get("code")!);
    }
  }, []);
  async function emailLogin(
    action: "send" | "verify" | "password",
    email: string,
    secret: string,
  ) {
    setError("");
    await api("/api/account/email/" + action, {
      email,
      ...(action === "password"
        ? { password: secret }
        : action === "verify"
          ? { otp: secret }
          : {}),
    });
    if (action !== "send") await refresh();
  }
  async function login(provider: string) {
    try {
      setBusy(true);
      const result = await api<{ url?: string }>("/api/auth/sign-in/social", {
        provider,
        callbackURL: location.href,
      });
      if (result.url) location.assign(result.url);
      else throw Error("登录配置尚未就绪");
    } catch (e) {
      setError(String(e));
      setBusy(false);
    }
  }
  return (
    <main className="account-page">
      <nav>
        <a className="brand" href="/">
          ♜ ReignHub
        </a>
        <a href="/chat">返回控制台 ↗</a>
      </nav>
      {loading ? (
        <p>正在加载账号…</p>
      ) : account ? (
        <>
          <h1>你好，{account.user.name}</h1>
          <p className="muted">{account.user.email}</p>
          <Button
            onClick={async () => {
              try {
                await api("/api/auth/sign-out", {});
                setAccount(undefined);
              } catch (e) {
                setError(String(e));
              }
            }}
          >
            退出登录
          </Button>
          {account.admin && (
            <a className="button ghost" href="/admin">
              权益管理
            </a>
          )}
          {account.canSetPassword && (
            <details className="account-section">
              <summary>设置登录密码</summary>
              <form
                className="email-sign-in"
                onSubmit={async (event) => {
                  event.preventDefault();
                  if (busy) return;
                  setBusy(true);
                  try {
                    await api("/api/account/password/set", {
                      password: newPassword,
                    });
                    setNewPassword("");
                    await refresh();
                    setError("登录密码已设置。");
                  } catch (cause) {
                    setError(
                      cause instanceof Error ? cause.message : "设置失败",
                    );
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                <label htmlFor="new-password">新密码</label>
                <input
                  id="new-password"
                  className="field"
                  type="password"
                  autoComplete="new-password"
                  minLength={8}
                  maxLength={128}
                  required
                  disabled={busy}
                  value={newPassword}
                  onChange={(ev) => setNewPassword(ev.target.value)}
                />
                <Button
                  variant="primary"
                  type="submit"
                  disabled={busy || newPassword.length < 8}
                >
                  保存密码
                </Button>
              </form>
            </details>
          )}
          <section className="account-section">
            <h2>当前权益 · {account.plan}</h2>
            <p className="muted">
              电脑 {account.devices.length} /{" "}
              {account.limits.maxHostDevices ?? "不限"} 台 · 官方中继
              {account.limits.allowRelay ? "已启用" : "未启用（优先直连）"}
            </p>
            <small className="muted">控制端登录不占被控端名额。</small>
          </section>
          <section className="account-section">
            <h2>设备同步</h2>
            <p className="muted">
              确认后，此设备将关联到 {account.user.email}。请核对 App
              中的授权码。
            </p>
            <input
              className="field"
              aria-label="设备授权码"
              placeholder="设备授权码"
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
            <Button
              disabled={busy || !code}
              onClick={async () => {
                try {
                  setBusy(true);
                  const result = await api<{
                    hostLinked: boolean;
                    hostLimit?: boolean;
                  }>("/api/account/device-login/approve", { code });
                  setCode("");
                  const url = new URL(location.href);
                  url.searchParams.delete("code");
                  history.replaceState(history.state, "", url);
                  setError(
                    result.hostLimit
                      ? "已登录控制端。被控端名额已满，可移除设备后重新关联。"
                      : "绑定成功，可返回客户端。",
                  );
                  await refresh();
                } catch (e) {
                  setError(String(e));
                } finally {
                  setBusy(false);
                }
              }}
            >
              确认绑定当前账号
            </Button>
            <table>
              <thead>
                <tr>
                  <th>设备</th>
                  <th>编号</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {account.devices.map((d) => (
                  <tr key={d.id}>
                    <td>{d.name}</td>
                    <td>
                      <a href={"/chat?device=" + d.connectionId}>
                        {d.connectionId} ↗
                      </a>
                    </td>
                    <td>
                      <Button
                        size="small"
                        variant="ghost"
                        onClick={async () => {
                          if (
                            !confirm("移除后，使用此账号关联建立的连接将断开。")
                          )
                            return;
                          try {
                            await api("/api/account/devices/remove", {
                              device: d.id,
                            });
                            await refresh();
                          } catch (e) {
                            setError(String(e));
                          }
                        }}
                      >
                        移除
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {account.controllers?.length ? (
              <>
                <h3>已登录的控制端</h3>
                {account.controllers.map((c) => (
                  <div key={c.id}>
                    {c.name}{" "}
                    <Button
                      size="small"
                      variant="ghost"
                      onClick={async () => {
                        if (!confirm("撤销此控制端的账号访问权限？")) return;
                        try {
                          await api("/api/account/devices/remove", {
                            device: c.id,
                          });
                          await refresh();
                        } catch (e) {
                          setError(String(e));
                        }
                      }}
                    >
                      移除
                    </Button>
                  </div>
                ))}
              </>
            ) : null}
          </section>
        </>
      ) : (
        <section className="sign-in-panel">
          <h1>登录 ReignHub</h1>
          <p className="muted">登录后即可访问同一账号下的设备。</p>
          {(providers.includes("email") || providers.includes("password")) && (
            <EmailSignIn
              methods={providers}
              busy={busy}
              setBusy={setBusy}
              request={emailLogin}
            />
          )}
          {providers.some((p) => p === "google" || p === "github") && (
            <div className="login-actions">
              {providers
                .filter((p) => p === "google" || p === "github")
                .map((p) => (
                  <Button key={p} disabled={busy} onClick={() => login(p)}>
                    {p === "github" ? "GitHub" : "Google"} 登录
                  </Button>
                ))}
            </div>
          )}
          {error && (
            <p className="account-message" role="status">
              {error}
            </p>
          )}
          {legal && (
            <p className="muted account-legal">
              登录即表示你同意 <a href={legal.terms}>服务条款</a>，并已阅读{" "}
              <a href={legal.privacy}>隐私政策</a>。
            </p>
          )}
        </section>
      )}
      {plans.length > 0 && (
        <section className="account-section">
          <h2>套餐</h2>
          {legal && (
            <p className="muted account-legal">
              付费订阅会按结账页所示周期自动续费，可通过「管理订阅与账单」在线取消。购买前请阅读{" "}
              <a href={legal.terms + "#section-7"}>取消与退款政策</a>、
              <a href={legal.terms}>服务条款</a> 和{" "}
              <a href={legal.privacy}>隐私政策</a>。
            </p>
          )}
          <div className="plan-grid">
            {plans.map((p) => (
              <article className="plan" key={p.id}>
                <h3>{p.name}</h3>
                <strong>{p.priceLabel}</strong>
                <p>
                  被控电脑：{p.limits.maxHostDevices ?? "不限"} 台<br />
                  {p.limits.allowRelay
                    ? `官方中继 · 同时 ${p.limits.maxConcurrentConnections ?? "不限"} 路`
                    : "直连，不含官方中继"}
                </p>
                {p.purchasable && (
                  <Button
                    disabled={busy || !account}
                    onClick={async () => {
                      try {
                        setBusy(true);
                        const r = await api<{ url: string }>(
                          "/api/billing/checkout",
                          { plan: p.id },
                        );
                        location.assign(r.url);
                      } catch (e) {
                        setError(String(e));
                        setBusy(false);
                      }
                    }}
                  >
                    升级套餐
                  </Button>
                )}
              </article>
            ))}
          </div>
          {account && (
            <Button
              variant="ghost"
              onClick={async () => {
                try {
                  const r = await api<{ url: string }>(
                    "/api/billing/portal",
                    {},
                  );
                  location.assign(r.url);
                } catch (e) {
                  setError(String(e));
                }
              }}
            >
              管理订阅与账单 ↗
            </Button>
          )}
        </section>
      )}
      {error && account && (
        <p className="account-message" role="status">
          {error}
        </p>
      )}
    </main>
  );
}
export function AdminPage() {
  const [data, setData] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    void api<{ plans: unknown }>("/api/admin/plans")
      .then((r) => setData(JSON.stringify(r.plans, null, 2)))
      .catch((e) => setError(String(e)));
  }, []);
  return (
    <main className="account-page">
      <nav>
        <a className="brand" href="/">
          ♜ ReignHub
        </a>
        <a href="/account">返回账号</a>
      </nav>
      <h1>权益管理</h1>
      <section className="account-section">
        <h2>当前套餐</h2>
        <pre>{data}</pre>
      </section>
      {error && <p className="account-message">{error}</p>}
    </main>
  );
}
