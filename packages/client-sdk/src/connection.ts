import {
  routes,
  SOCKET_PROTOCOL,
  type Frame,
  type Identity,
  type RPCRequest,
  type ServiceConfig,
} from "@reignhub/protocol";
import {
  Cipher,
  b64,
  decode,
  hex,
  random,
  sum,
  un64,
  url64,
  utf8,
} from "./crypto";
import {
  CONNECTION_STAGE_TIMEOUT,
  progressStep,
  recordProgress,
  type ConnectionProgress,
  type ProgressObserver,
} from "./progress";
import type { SavedDevice } from "./cache";
interface Reply {
  code?: string;
  id: string;
  result: unknown;
  error?: string;
  revision?: string;
  notModified?: boolean;
}
interface Pending {
  touch?: () => void;
  resolve: (v: unknown) => void;
  reject: (e: Error) => void;
  timer?: ReturnType<typeof setTimeout>;
  cleanup?: () => void;
  key: string;
  cached?: { revision: string; value: unknown };
}
export class Connection extends EventTarget {
  private signal!: WebSocket;
  private opening = new Set<WebSocket>();
  private relay?: WebSocket;
  private pc?: RTCPeerConnection;
  private dc?: RTCDataChannel;
  private peer = "";
  private nonce = hex(random());
  private base!: Cipher;
  private cipher?: Cipher;
  private pending = new Map<string, Pending>();
  private snapshots = new Map<string, { revision: string; value: unknown }>();
  private chunks = new Map<
    string,
    {
      request: string;
      encoding: string;
      rawSize: number;
      parts: (Uint8Array | undefined)[];
      size: number;
      updated: number;
    }
  >();
  private candidates: RTCIceCandidateInit[] = [];
  private chain = Promise.resolve();
  private frameQueues: {
    run: () => Promise<void>;
    resolve: () => void;
    reject: (error: unknown) => void;
  }[][] = [[], [], []];
  private sending = false;
  private urgentFrames = 0;
  private otherFrames = 0;
  private tick?: ReturnType<typeof setInterval>;
  private closed = false;
  private established = false;
  private handshook!: () => void;
  private failHandshake!: (e: Error) => void;
  private heartbeat = Date.now();
  private pulses = 0;
  progress: ConnectionProgress[] = [];
  path = "Connecting";
  allowRelay = false;
  constructor(
    public saved: SavedDevice,
    private identity: Identity,
    private config: ServiceConfig,
    private observe?: ProgressObserver,
  ) {
    super();
  }
  private report(
    id: string,
    stage: string,
    message: string,
    detail = "",
    state: ConnectionProgress["state"] = "running",
  ) {
    const step = progressStep(id, stage, message, detail, state);
    const next = recordProgress(this.progress, step);
    if (next === this.progress) return;
    this.progress = next;
    this.observe?.(step);
    this.dispatchEvent(new Event("progress"));
  }
  private update(path: string) {
    if (this.path !== path) {
      this.path = path;
      this.dispatchEvent(new Event("change"));
    }
  }
  private endpoint(path: string) {
    const u = new URL(path, this.saved.service);
    u.protocol = u.protocol === "https:" ? "wss:" : "ws:";
    return u.origin + u.pathname;
  }
  private async socket(path: string, peer?: string) {
    if (this.closed) throw Error("连接已取消");
    const u = new URL(this.saved.service);
    u.protocol = u.protocol === "https:" ? "wss:" : "ws:";
    u.pathname = path;
    u.searchParams.set("id", this.saved.id);
    u.searchParams.set("role", "client");
    if (peer) u.searchParams.set("peer", peer);
    const ws = new WebSocket(u, [
      SOCKET_PROTOCOL,
      "auth." + url64(utf8(this.identity.token)),
    ]);
    this.opening.add(ws);
    const welcome = await new Promise<Frame>((resolve, reject) => {
      const timeout = setTimeout(() => {
        ws.close();
        reject(
          Error(
            `${path === routes.relay ? "备用中继通道" : "信令连接"}超时（2 分钟）`,
          ),
        );
      }, CONNECTION_STAGE_TIMEOUT);
      ws.onerror = () => {
        clearTimeout(timeout);
        ws.close();
        reject(Error("连接服务失败，请检查服务地址和网络"));
      };
      ws.onmessage = (e) => {
        clearTimeout(timeout);
        try {
          const f = JSON.parse(e.data) as Frame;
          if (f.kind !== "welcome" || f.protocol !== 1) throw Error();
          resolve(f);
        } catch {
          ws.close();
          reject(Error("不支持的服务协议"));
        }
      };
      ws.onclose = () => {
        clearTimeout(timeout);
        reject(Error("服务拒绝连接，请检查配对和账号权益"));
      };
    }).finally(() => this.opening.delete(ws));
    if (this.closed) {
      ws.close();
      throw Error("连接已取消");
    }
    return { ws, welcome };
  }
  async connect() {
    try {
      this.report(
        "signal",
        "signal",
        "建立信令连接",
        this.endpoint(routes.signal),
      );
      const { ws, welcome } = await this.socket(routes.signal);
      this.report(
        "signal",
        "signal",
        "信令连接已建立",
        this.endpoint(routes.signal),
        "success",
      );
      this.signal = ws;
      this.peer = welcome.peer!;
      this.allowRelay = !!welcome.allowRelay;
      this.base = await Cipher.create(
        await sum("reign/p2p/peer/" + this.peer + "/" + this.saved.key),
        1,
        true,
      );
      ws.onmessage = (e) => {
        this.heartbeat = Date.now();
        if (e.data === "reign:pong") return;
        this.chain = this.chain
          .then(() => this.receive(JSON.parse(e.data) as Frame))
          .catch(() => this.close("连接数据校验失败"));
      };
      ws.onclose = () => this.signalLost("信令连接已断开");
      ws.onerror = () => ws.close();
      this.tick = setInterval(() => {
        if (Date.now() - this.heartbeat > 85000) {
          ws.close();
          this.signalLost("连接心跳超时");
          return;
        }
        if (this.signal.readyState === WebSocket.OPEN) {
          this.signal.send("reign:ping");
          if (++this.pulses % 3 === 0)
            this.signal.send(JSON.stringify({ kind: "renew" }));
        }
        if (this.relay?.readyState === WebSocket.OPEN)
          this.relay.send("reign:ping");
      }, 25000);
      if (this.allowRelay) void this.openDataRelay();
      const ready = new Promise<void>((resolve, reject) => {
        this.handshook = resolve;
        this.failHandshake = reject;
      });
      const timeout = setTimeout(
        () => this.failHandshake(Error("配对握手超时")),
        CONNECTION_STAGE_TIMEOUT,
      );
      this.report("handshake", "handshake", "与被控设备建立加密握手");
      await this.sendFrame("hello", { rpc: 3, clientNonce: this.nonce });
      try {
        await ready;
        this.report(
          "handshake",
          "handshake",
          "端到端加密握手完成",
          "已验证设备配对密钥",
          "success",
        );
      } finally {
        clearTimeout(timeout);
      }
      this.update("Connecting · direct first");
      if (typeof RTCPeerConnection !== "undefined") {
        void this.offer().catch((e) => {
          this.report(
            "ice",
            "udp",
            "WebRTC 探测未能启动",
            e instanceof Error ? e.message : String(e),
            "warning",
          );
        });
      } else {
        this.report(
          "ice",
          "udp",
          "当前浏览器不提供 WebRTC",
          "使用可用的服务中继",
          "warning",
        );
      }
      this.report("direct", "direct", "正在验证直连或中继路径");
      await this.waitPath();
      this.report("verify", "verify", "正在验证设备响应");
      await this.call({ method: "network" }, CONNECTION_STAGE_TIMEOUT);
      this.established = true;
      this.report(
        "connected",
        "connected",
        "设备连接成功",
        this.path,
        "success",
      );
      return this;
    } catch (e) {
      this.close();
      throw e;
    }
  }
  private async openDataRelay() {
    this.report(
      "relay-ready",
      "relay",
      "准备备用中继通道",
      this.endpoint(routes.relay),
    );
    try {
      const data = await this.socket(routes.relay, this.peer);
      this.relay = data.ws;
      this.report(
        "relay-ready",
        "relay",
        "备用中继通道已就绪",
        this.endpoint(routes.relay) + " · 优先直连",
        "success",
      );
      this.relay.onclose = () => {
        this.relay = undefined;
        if (!this.dc || this.dc.readyState !== "open") {
          if (this.established) this.close("数据连接已断开");
          else this.update("Disconnected");
        }
      };
      this.relay.onmessage = (e) => {
        if (e.data !== "reign:pong") {
          try {
            const f = JSON.parse(e.data) as Frame;
            if (f.kind === "error")
              this.dispatchEvent(new CustomEvent("error", { detail: f.error }));
          } catch {}
        }
      };
    } catch (error) {
      if (this.closed) return;
      this.report(
        "relay-ready",
        "relay",
        "备用中继暂不可用，继续尝试 P2P 直连",
        error instanceof Error ? error.message : String(error),
        "warning",
      );
    }
  }
  private signalLost(reason: string) {
    if (this.closed) return;
    if (!this.established || this.dc?.readyState !== "open") {
      this.close(reason);
      return;
    }
    clearInterval(this.tick);
    this.relay?.close();
    this.report(
      "signal-lost",
      "signal",
      reason,
      "已建立的 WebRTC 直连继续使用",
      "warning",
    );
  }
  private aad(kind: string, out = true) {
    return "reign/p2p/" + this.peer + "/" + (out ? "c2h" : "h2c") + "/" + kind;
  }
  private async sendFrame(kind: string, value: unknown) {
    const cipher = kind === "hello" ? this.base : this.cipher;
    if (!cipher) throw Error("握手未完成");
    const body = b64(
      await cipher.seal(this.aad(kind), utf8(JSON.stringify(value))),
    );
    this.signal.send(JSON.stringify({ peer: this.peer, kind, body }));
  }
  private async receive(f: Frame) {
    if (f.kind === "error") throw Error(f.error);
    if (f.kind === "lease") return;
    if (!f.body) return;
    const cipher = f.kind === "hello" ? this.base : this.cipher;
    if (!cipher) return;
    const plain = await cipher.open(this.aad(f.kind, false), un64(f.body));
    if (f.kind === "hello") {
      if (this.cipher) return;
      const h = JSON.parse(decode(plain)) as {
        rpc: number;
        clientNonce: string;
        hostNonce: string;
      };
      if (h.rpc !== 3)
        throw Error("设备协议版本不一致，请同时更新控制端和被控端");
      if (h.clientNonce !== this.nonce || !/^[a-f0-9]{64}$/.test(h.hostNonce))
        throw Error("无效配对握手");
      this.cipher = await Cipher.create(
        await sum(
          "reign/p2p/session/" +
            this.saved.key +
            "/" +
            this.peer +
            "/" +
            h.clientNonce +
            "/" +
            h.hostNonce,
        ),
      );
      this.handshook();
      return;
    }
    if (f.kind === "signal") {
      const s = JSON.parse(decode(plain)) as RTCSessionDescriptionInit & {
        candidate?: RTCIceCandidateInit;
      };
      if ((s.type as string) === "candidate") {
        if (s.candidate) {
          if (this.pc?.remoteDescription)
            await this.pc.addIceCandidate(s.candidate);
          else if (this.candidates.length < 256)
            this.candidates.push(s.candidate);
        }
        return;
      }
      if (s.type === "answer" && this.pc) {
        await this.pc.setRemoteDescription(s);
        for (const c of this.candidates) await this.pc.addIceCandidate(c);
        this.candidates = [];
      }
      return;
    }
    if (f.kind === "chunk") await this.assemble(plain);
    else if (f.kind === "data") this.response(plain);
  }
  private async offer() {
    for (const url of this.config.publicStun)
      this.report(
        "stun:" + url,
        "udp",
        "公共 STUN 节点",
        url + " · 并行探测",
        "info",
      );
    for (const url of this.config.selfStun)
      this.report(
        "stun:" + url,
        "udp",
        "配置的 STUN 节点",
        url + " · 并行探测",
        "info",
      );
    this.report(
      "tcp",
      "tcp",
      "浏览器使用 WebRTC 直连",
      "原生 TCP 打洞由桌面端和移动端支持",
      "info",
    );
    this.report("ice", "udp", "收集 UDP / IPv6 直连候选地址");
    this.pc = new RTCPeerConnection({
      iceServers: [
        ...new Set([...this.config.publicStun, ...this.config.selfStun]),
      ]
        .filter((x) => x.startsWith("stun:"))
        .map((url) => ({ urls: url })),
    });
    const pc = this.pc;
    pc.onicecandidate = (e) => {
      if (!e.candidate) {
        this.report(
          "ice",
          "udp",
          "UDP 候选地址收集结束",
          "正在检查可用直连路径",
          "success",
        );
        return;
      }
      this.report(
        "candidate:" +
          e.candidate.foundation +
          e.candidate.address +
          e.candidate.port,
        "udp",
        "发现 UDP 直连候选",
        `${e.candidate.type} · ${e.candidate.address || "本地地址"}:${e.candidate.port} · ${e.candidate.protocol}`,
        "info",
      );
      if (e.candidate)
        void this.sendFrame("signal", {
          type: "candidate",
          candidate: e.candidate.toJSON(),
        }).catch(() => {});
    };
    pc.onicegatheringstatechange = () => {
      if (pc.iceGatheringState === "complete")
        void pc
          .getStats()
          .then((stats) =>
            stats.forEach((stat) => {
              if (stat.type === "local-candidate" && stat.url && !this.closed)
                this.report(
                  "stun:" + stat.url,
                  "udp",
                  "STUN 节点已返回公网映射",
                  `${stat.url} → ${stat.address || stat.ip || "公网地址"}:${stat.port}`,
                  "success",
                );
            }),
          )
          .catch(() => {});
    };
    pc.onicecandidateerror = (e) =>
      this.report(
        "stun-error:" + e.url,
        "udp",
        "STUN 节点探测未成功",
        `${e.url} · ${e.errorCode} ${e.errorText}`,
        "warning",
      );
    pc.onconnectionstatechange = () => {
      if (pc.connectionState === "connecting")
        this.report("ice-check", "udp", "正在验证 WebRTC 直连路径");
      if (pc.connectionState === "failed")
        this.report(
          "ice-check",
          "udp",
          "WebRTC 直连暂未建立",
          "使用可用的服务中继",
          "warning",
        );
      if (["failed", "disconnected", "closed"].includes(pc.connectionState)) {
        if (this.relay?.readyState === WebSocket.OPEN)
          this.update("Relay · WebSocket");
        else if (this.established) this.close("直连已断开，且没有可用中继");
        else this.update("Disconnected");
      }
    };
    this.dc = pc.createDataChannel("reign.p2p", { ordered: true });
    this.dc.binaryType = "arraybuffer";
    this.dc.onopen = () => {
      this.update("P2P · WebRTC");
      this.report(
        "path:webrtc",
        "direct",
        "已切换为 WebRTC 直连",
        "端到端加密数据通道已建立",
        "success",
      );
      void pc
        .getStats()
        .then((stats) => {
          stats.forEach((stat) => {
            if (stat.type === "transport" && stat.selectedCandidatePairId) {
              const pair = stats.get(stat.selectedCandidatePairId),
                local = stats.get(pair?.localCandidateId),
                remote = stats.get(pair?.remoteCandidateId);
              if (
                local &&
                remote &&
                !this.closed &&
                this.dc?.readyState === "open"
              )
                this.report(
                  "path:webrtc",
                  "direct",
                  "已切换为 WebRTC 直连",
                  `${local.protocol} · ${local.address || local.ip || local.candidateType}:${local.port} → ${remote.address || remote.ip || remote.candidateType}:${remote.port}`,
                  "success",
                );
            }
          });
        })
        .catch(() => {});
    };
    this.dc.onclose = () =>
      this.update(this.relay ? "Relay · WebSocket" : "Disconnected");
    this.dc.onmessage = (e) => {
      this.chain = this.chain
        .then(async () => {
          if (e.data instanceof ArrayBuffer && this.cipher)
            await this.assemble(
              await this.cipher.open(
                this.aad("chunk", false),
                new Uint8Array(e.data),
              ),
            );
        })
        .catch(() => this.close("加密数据异常"));
    };
    await pc.setLocalDescription(await pc.createOffer());
    await this.sendFrame("signal", pc.localDescription!.toJSON());
  }
  private async assemble(plain: Uint8Array) {
    const c = JSON.parse(decode(plain)) as {
      r: string;
      e: string;
      s: number;
      id: string;
      i: number;
      n: number;
      b: string;
    };
    if (
      typeof c.r !== "string" ||
      !c.r ||
      c.r.length > 256 ||
      !["identity", "gzip"].includes(c.e) ||
      !Number.isInteger(c.s) ||
      c.s < 1 ||
      c.s > 16 * 1024 * 1024 ||
      !Number.isInteger(c.n) ||
      c.n < 1 ||
      c.n > 4096 ||
      !Number.isInteger(c.i) ||
      c.i < 0 ||
      c.i >= c.n
    )
      return;
    const part = un64(c.b);
    if (part.length > 4096) return;
    if (!this.pending.has(c.r)) {
      this.discardResponse(c.r);
      return;
    }
    this.pending.get(c.r)?.touch?.();
    for (const [k, v] of this.chunks)
      if (Date.now() - v.updated > CONNECTION_STAGE_TIMEOUT)
        this.chunks.delete(k);
    let a = this.chunks.get(c.id);
    if (!a) {
      if (this.chunks.size >= 64) return;
      a = {
        request: c.r,
        encoding: c.e,
        rawSize: c.s,
        parts: Array(c.n),
        size: 0,
        updated: Date.now(),
      };
      this.chunks.set(c.id, a);
    }
    if (
      a.request !== c.r ||
      a.encoding !== c.e ||
      a.rawSize !== c.s ||
      a.parts.length !== c.n ||
      a.parts[c.i]
    )
      return;
    a.updated = Date.now();
    a.parts[c.i] = part;
    a.size += part.length;
    if (a.size > 16 * 1024 * 1024) {
      this.chunks.delete(c.id);
      return;
    }
    if (a.parts.filter(Boolean).length !== c.n) return;
    const full = new Uint8Array(a.size);
    let at = 0;
    for (const p of a.parts) {
      full.set(p!, at);
      at += p!.length;
    }
    this.chunks.delete(c.id);
    let raw = full;
    if (a.encoding === "gzip") {
      const reader = new Blob([full.buffer])
        .stream()
        .pipeThrough(new DecompressionStream("gzip"))
        .getReader();
      const parts: Uint8Array[] = [];
      let size = 0;
      try {
        for (;;) {
          const part = await reader.read();
          if (part.done) break;
          size += part.value.byteLength;
          if (size > a.rawSize) throw Error("解压后的响应超过限制");
          parts.push(part.value);
        }
      } finally {
        await reader.cancel();
      }
      raw = new Uint8Array(size);
      let offset = 0;
      for (const part of parts) {
        raw.set(part, offset);
        offset += part.length;
      }
    }
    if (raw.byteLength !== a.rawSize) throw Error("响应长度不匹配");
    this.response(raw);
  }
  private discardResponse(request: string) {
    for (const [id, value] of this.chunks)
      if (value.request === request) this.chunks.delete(id);
  }
  private response(raw: Uint8Array) {
    const r = JSON.parse(decode(raw)) as Reply;
    const p = this.pending.get(r.id);
    if (!p) return;
    clearTimeout(p.timer);
    p.cleanup?.();
    this.pending.delete(r.id);
    if (r.error) {
      if (r.code === "CONNECTION_AUTH_EXPIRED") {
        this.close("连接授权需要续期，请重新连接");
        p.reject(Error("连接授权需要续期，请重新连接"));
        return;
      }
      p.reject(Error(r.error));
      return;
    }
    if (r.notModified) {
      if (!p.cached || p.cached.revision !== r.revision) {
        p.reject(Error("缓存版本不匹配"));
        return;
      }
      r.result = p.cached.value;
    } else if (p.key && r.revision) {
      if (this.snapshots.size >= 32)
        this.snapshots.delete(this.snapshots.keys().next().value!);
      this.snapshots.set(p.key, { revision: r.revision, value: r.result });
    }
    p.resolve(r.result);
  }
  private async waitPath() {
    const deadline = Date.now() + CONNECTION_STAGE_TIMEOUT;
    while (!this.closed && this.dc?.readyState !== "open" && !this.relay) {
      if (Date.now() > deadline)
        throw Error(
          this.allowRelay
            ? "直连失败，备用中继通道也未能建立，请查看连接详情"
            : "直连失败，当前服务未提供中继权限",
        );
      await new Promise((r) => setTimeout(r, 30));
    }
    if (this.closed) throw Error("连接已断开");
  }
  private enqueueFrame(priority: number, run: () => Promise<void>) {
    return new Promise<void>((resolve, reject) => {
      this.frameQueues[priority].push({ run, resolve, reject });
      if (!this.sending) void this.flushFrames();
    });
  }
  private async flushFrames() {
    this.sending = true;
    try {
      for (;;) {
        let priority =
          this.urgentFrames < 8 && this.frameQueues[0].length ? 0 : -1;
        if (priority < 0 && this.otherFrames >= 8 && this.frameQueues[2].length)
          priority = 2;
        if (priority < 0)
          priority = this.frameQueues[1].length
            ? 1
            : this.frameQueues[0].length
              ? 0
              : this.frameQueues[2].length
                ? 2
                : -1;
        if (priority < 0) break;
        const job = this.frameQueues[priority].shift()!;
        if (this.closed) {
          job.reject(Error("连接已关闭"));
          continue;
        }
        this.urgentFrames = priority === 0 ? this.urgentFrames + 1 : 0;
        this.otherFrames = priority === 2 ? 0 : this.otherFrames + 1;
        try {
          await job.run();
          job.resolve();
        } catch (error) {
          job.reject(error);
        }
      }
    } finally {
      this.sending = false;
    }
  }
  private async send(
    raw: Uint8Array,
    requestID: string,
    method: string,
    active = () => true,
  ) {
    if (raw.length > 1024 * 1024) throw Error("请求过大");
    await this.waitPath();
    if (this.closed || !this.cipher) throw Error("连接已断开");
    const rawSize = raw.length;
    let encoding = "identity";
    if (rawSize >= 1024) {
      const compressed = new Uint8Array(
        await new Response(
          new Blob([raw.slice().buffer])
            .stream()
            .pipeThrough(new CompressionStream("gzip")),
        ).arrayBuffer(),
      );
      if (compressed.length * 8 <= rawSize * 7) {
        raw = compressed;
        encoding = "gzip";
      }
    }
    const priority = [
      "transcript",
      "attachment.read",
      "attachment.write",
    ].includes(method)
      ? 2
      : ["providers", "sessions", "sync", "models"].includes(method)
        ? 1
        : 0;
    const id = hex(random(16));
    const n = Math.max(1, Math.ceil(raw.length / 4096));
    for (let i = 0; i < n; i++) {
      const part = utf8(
        JSON.stringify({
          r: requestID,
          e: encoding,
          s: rawSize,
          id,
          i,
          n,
          b: b64(raw.slice(i * 4096, (i + 1) * 4096)),
        }),
      );
      if (!active()) return;
      await this.enqueueFrame(priority, async () => {
        if (!active() || this.closed || !this.cipher) return;
        const sealed = await this.cipher.seal(this.aad("chunk"), part);
        if (this.dc?.readyState === "open") {
          const until = Date.now() + 8000;
          while (this.dc.bufferedAmount > 512 * 1024) {
            if (Date.now() > until || this.closed)
              throw Error("直连发送阻塞，请刷新确认结果后再重试");
            await new Promise((r) => setTimeout(r, 10));
          }
          this.dc.send(sealed as unknown as ArrayBuffer);
        } else if (this.relay?.readyState === WebSocket.OPEN) {
          this.update("Relay · WebSocket");
          this.report(
            "path:relay",
            "relay",
            "正在使用加密中继",
            "直连尚未建立，先通过服务中继；后台继续尝试直连",
            "success",
          );
          this.relay.send(
            JSON.stringify({
              peer: this.peer,
              kind: "chunk",
              body: b64(sealed),
            }),
          );
        } else throw Error("发送中断，请刷新确认结果后再重试");
      });
    }
  }
  async call<T = unknown>(
    request: RPCRequest,
    timeout?: number,
    signal?: AbortSignal,
  ): Promise<T> {
    if (this.closed) throw Error("连接已断开");
    signal?.throwIfAborted();
    const r: RPCRequest & { id: string } = {
      ...request,
      id: crypto.randomUUID(),
    };
    const readOnly = [
      "providers",
      "network",
      "sessions",
      "transcript",
      "approvals",
      "sync",
      "models",
      "turn.status",
    ].includes(r.method);
    const cancelRead = () => {
      this.discardResponse(r.id);
      if (!readOnly && !["watch", "attachment.read"].includes(r.method)) return;
      const request = {
        id: crypto.randomUUID(),
        method: "request.cancel",
        cancel: r.id,
        provider: r.provider,
        session: r.session,
      };
      void this.send(
        utf8(JSON.stringify(request)),
        request.id,
        request.method,
      ).catch(() => {});
    };
    const key = readOnly
      ? JSON.stringify({ ...r, id: undefined, revision: undefined })
      : "";
    const cached = this.snapshots.get(key);
    if (cached) r.revision = cached.revision;
    const result = new Promise<T>((resolve, reject) => {
      // Bound silence per read, while allowing a progressing transfer to run
      // longer. Explicit setup deadlines remain absolute.
      const timer =
        timeout === undefined
          ? undefined
          : setTimeout(() => {
              this.pending.get(r.id)?.cleanup?.();
              this.pending.delete(r.id);
              cancelRead();
              reject(Error("请求超时，请刷新确认操作结果后再重试"));
            }, timeout);
      let idleTimer: ReturnType<typeof setTimeout> | undefined;
      const touch =
        readOnly || ["watch", "attachment.read"].includes(r.method)
          ? () => {
              clearTimeout(idleTimer);
              idleTimer = setTimeout(() => {
                clearTimeout(timer);
                this.pending.get(r.id)?.cleanup?.();
                this.pending.delete(r.id);
                cancelRead();
                reject(
                  Error("设备读取连续 30 秒没有进展，已取消本次读取，请重试"),
                );
              }, 30000);
            }
          : undefined;
      const abort = () => {
        clearTimeout(timer);
        this.pending.get(r.id)?.cleanup?.();
        this.pending.delete(r.id);
        cancelRead();
        reject(signal?.reason ?? new DOMException("请求已取消", "AbortError"));
      };
      signal?.addEventListener("abort", abort, { once: true });
      this.pending.set(r.id, {
        resolve: resolve as (v: unknown) => void,
        reject,
        timer,
        cleanup: () => {
          clearTimeout(idleTimer);
          signal?.removeEventListener("abort", abort);
        },
        touch,
        key,
        cached,
      });
      touch?.();
    });
    void this.send(utf8(JSON.stringify(r)), r.id, r.method, () =>
      this.pending.has(r.id),
    ).catch((e) => {
      const p = this.pending.get(r.id);
      if (p) {
        clearTimeout(p.timer);
        p.cleanup?.();
        this.pending.delete(r.id);
        p.reject(e as Error);
      }
    });
    return result;
  }
  close(reason = "Disconnected") {
    if (this.closed) return;
    this.closed = true;
    if (reason !== "Disconnected")
      this.report(
        "disconnected",
        "connection",
        "连接已断开",
        reason,
        "warning",
      );
    clearInterval(this.tick);
    this.failHandshake?.(Error(reason));
    for (const socket of this.opening) socket.close();
    this.opening.clear();
    this.signal?.close();
    this.relay?.close();
    this.pc?.close();
    for (const p of this.pending.values()) {
      clearTimeout(p.timer);
      p.cleanup?.();
      p.reject(Error(reason));
    }
    this.pending.clear();
    this.update("Disconnected");
    this.dispatchEvent(new CustomEvent("disconnect", { detail: reason }));
  }
}
