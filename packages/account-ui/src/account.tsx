"use client";
import { useEffect, useState } from "react";
import { Button } from "./ui";
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
  const [handoff, setHandoff] = useState(false);
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
      setHandoff(true);
    }
  }, []);
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
              在原生客户端点击「登录与绑定账号」，将显示的授权码填在这里。同账号的控制端可直接访问已关联的被控端。
            </p>
            <input
              className="field"
              placeholder="原生客户端授权码"
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
        <>
          <h1>登录 ReignHub</h1>
          <p className="muted">
            登录后可从同一账号的控制端直接访问已关联的被控端。
          </p>
          {legal && (
            <p className="muted account-legal">
              登录即表示你同意 <a href={legal.terms}>服务条款</a>，并已阅读{" "}
              <a href={legal.privacy}>隐私政策</a>。
            </p>
          )}
          <div className="login-actions">
            {providers.map((p) => (
              <Button key={p} disabled={busy} onClick={() => login(p)}>
                {p === "github" ? "GitHub" : "Google"} 登录
              </Button>
            ))}
          </div>
        </>
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
      {error && (
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
  const [user, setUser] = useState("");
  const [override, setOverride] = useState("");
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
      <p className="muted">
        修改会在下一次授权续期生效。已有本地 Agent 任务不会被终止。
      </p>
      <section className="account-section">
        <h2>套餐配置</h2>
        <textarea
          className="field admin-editor"
          value={data}
          onChange={(e) => setData(e.target.value)}
        />
        <Button
          onClick={async () => {
            try {
              await api("/api/admin/plans", { plans: JSON.parse(data) });
              setError("已保存");
            } catch (e) {
              setError(String(e));
            }
          }}
        >
          保存套餐
        </Button>
      </section>
      <section className="account-section">
        <h2>账号覆盖</h2>
        <input
          className="field"
          placeholder="Better Auth 用户 ID"
          value={user}
          onChange={(e) => setUser(e.target.value)}
        />
        <textarea
          className="field"
          placeholder='{"maxHostDevices":5,"maxAgentsPerDevice":null,"maxConcurrentConnections":null,"allowRelay":true}'
          value={override}
          onChange={(e) => setOverride(e.target.value)}
        />
        <Button
          onClick={async () => {
            try {
              await api("/api/admin/override", {
                user,
                limits: override ? JSON.parse(override) : null,
              });
              setError("已保存");
            } catch (e) {
              setError(String(e));
            }
          }}
        >
          保存覆盖
        </Button>
      </section>
      {error && <p className="account-message">{error}</p>}
    </main>
  );
}
