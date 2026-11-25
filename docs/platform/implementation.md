# 服务平台实施约定

公共仓库保留官网与文档源码，但开源 Go/Worker 部署及发布归档只包含 NSGo Web 远控客户端和服务核心，根地址统一跳转 `/chat/`。官网通过独立 CI 构建与发布，不进入自部署包。私有仓库通过 submodule 组合公共核心、Better Auth、Waffo 和可配置权益，官方只部署一个 Worker，绑定 D1 和 Durable Objects。另提供独立 Go 服务 `reignhub-server`，使用本地 SQLite，内嵌 NSGo Web 控制台并默认开启 TCP/UDP 探测；不依赖 Cloudflare。通过 `config.json` 可关闭各组件、调整独立监听地址，也能以 `{"services":{"control":{"enable":false},"relay":{"enable":false}}}` 仅运行探测服务配合 Worker。Go 与 Worker 复用 `/p2p/*`、设备配对、签名租约和加密帧协议，控制端不按服务端实现分叉。

不兼容旧接口；所有连接接口使用 `/p2p/`。信令 `/p2p/signal` 与业务中继 `/p2p/relay` 分离。客户端只配置一个服务 origin，公共和自建 STUN 并行参与候选发现；原生端并行尝试 WebRTC/UDP 和 TCP，浏览器使用 WebRTC。官方免费账号仅允许直连，付费账号可回退官方 WSS；自部署无商业配额。移除 cloudflared/Tunnel。

免费账号默认 3 台被控设备、每台 1 种 Agent 产品，Codex App/CLI 合并为 Codex 产品。控制端不占被控设备名额，会话不限。套餐、账号覆盖值均在私有业务层配置。账号配额由 DO 串行分配，D1 持久化最终资源归属；失败必须补偿，重复申请不能重复占位。租约约束设备、控制端、Agent 产品和线路，被控端在统一 RPC 入口验证，覆盖直连和中继，不能影响本地 Agent 任务。

设备绑定必须同时证明设备凭据和账号会话；登录同步目录，不替代配对密钥。密钥保留本地，浏览器使用 IndexedDB；聊天和文件不存进 D1。支付只信任验证并去重后的 Waffo Webhook及平台查询，不信任浏览器回跳。

上线还需运营者提供 Cloudflare 资源授权、GitHub/Google OAuth 凭据、Better Auth Secret、Waffo 店铺/API/Webhook 配置和产品映射。开发测试使用本地资源与隔离 Agent fixture，不启动用户真实 Agent。
