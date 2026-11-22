# CI 与 Release

GitHub Actions 工作流：[CI and Release](../.github/workflows/ci.yml)。客户端只构建共享 NSGo / Go 实现，服务分别验证 Go 和 Cloudflare Worker。客户端构建统一使用根目录 `scripts/build.sh`，默认产物在根目录 `dist/`，发布清单在 `dist/release/`。

## 触发

- 提交到 `main`、发起或更新目标为 `main` 的 PR：跑测试、构建和上传临时产物。
- Actions 页面手动运行：执行同样的验证，不创建版本 tag 或 Release。
- 推送 `vX.Y.Z` 或 `vX.Y.Z-rc.N` tag：全部验证成功后发布 GitHub Release。带 `-` 的版本标为 prerelease。

非 tag 构建版本为 `nsgo.yaml` 的版本 + `-dev.<run_number>`；正式版本取 tag，Apple build number / Android versionCode 使用 workflow run number。重打包 Android 原生二维码组件时也传递相同版本，不回退到配置文件中的旧版本。

## 验证与产物

Linux 和 macOS 分别运行两个 Go module 的 `go vet`、`go test -race` 与覆盖率。额外检查 Go 格式、构建脚本、版本解析、发布清单与校验和逻辑。

| 产物                                           | 格式                                                                                         |
| ---------------------------------------------- | -------------------------------------------------------------------------------------------- |
| macOS arm64 / x64                              | `.dmg`，拖入 Applications 安装                                                               |
| Windows x64 / arm64                            | `-setup.exe`，包含快捷方式与卸载程序                                                         |
| Linux x64 / arm64 桌面                         | `.deb`、`.pkg.tar.gz`（Arch）与 `.AppImage`，运行需要 GTK 4                                  |
| Android arm64                                  | 含原生 QR host 的 APK                                                                        |
| iOS                                            | `-unsigned.ipa`，需自行签名才能安装                                                          |
| iOS Simulator                                  | 编译验证，不作为设备安装包发布                                                               |
| Go 服务 Linux / macOS / Windows，amd64 / arm64 | `reignhub-server-<version>-<os>-<arch>.tar.gz`（Windows 为 ZIP），含 Web、SQLite、中继和探测 |

PR 和 main 构建的产物在对应 Actions run 的 Artifacts 中，保留 14 天；覆盖率保留 7 天。每次完整构建都会把公开产物合并到 `release-bundle` artifact，验证 12 个客户端安装包和 6 个服务端归档及其 SHA-256。发布任务按验证清单上传这 18 个文件，GitHub 为每个附件提供 SHA-256。缺失、额外文件或校验和错误都会阻止发布。

Go server job 复用 Web job 上传的控制台静态资源，交叉编译六个平台，并通过随机端口验证打包二进制的 Web 分片、SQLite、TCP/UDP，以及 TypeScript 控制端到隔离 Go Agent 的加密中继互通。另构建并启动非 root、根文件系统只读的 Docker 服务，验证同样的加密客户端互通。

Web job 只构建 NSGo Web 和独立开源 Worker，并使用迁移后的本地 D1 验证原生客户端互通、根路径跳转、分片资源与官网路径 404。官网由独立的 `site.yml` 工作流使用 `pnpm build:site` 构建、验证；仅官方仓库开启 `REIGNHUB_DEPLOY_OFFICIAL_SITE=true` 并配置 Cloudflare secrets 后发布，不进入开源归档。同一个 Linux amd64 服务程序读取 `{"services":{"control":{"enable":false},"relay":{"enable":false}}}` 的 JSON 配置并在随机本地端口启动，验证 TCP/UDP 探测，不承载 Web 或账号服务。

## 配置（均可选）

仓库 Settings → Secrets and variables → Actions：

| 名称                         | 类型     | 用途                                                                                 |
| ---------------------------- | -------- | ------------------------------------------------------------------------------------ |
| `REIGNHUB_SERVER_URL`        | Variable | 原生客户端内置默认服务地址；未配置为 `https://reignhub.app`。Web 始终使用当前 origin |
| `ANDROID_SIGNING_PEM_BASE64` | Secret   | 可选的固定 Android 签名身份，使用 NSGo PEM 格式（RSA private key + certificate）     |

没有签名 Secret 也能完成流水线。Android 包名会加 `-debug.apk`，使用本次临时生成的调试身份，无法覆盖安装另一签名的旧包。需要跨版本覆盖安装时，将已有 NSGo `.nsgo/android/debug.pem`（或同格式的专用发布身份）Base64 后保存到上述 Secret，之后持续使用同一身份。不要提交密钥文件；流水线不会把 `.nsgo` 或密钥上传为产物，PR 也不会恢复这个 Secret。这不是 Google Play 上传密钥 / JKS 配置。

macOS 目前是 ad hoc 签名、未公证；iOS 为未签名包。Apple Developer 证书、Provisioning、Notarization 和商店发布尚未接入，不能把 CI 构建成功视为可直接上架。

版本 tag 的发布必须配置固定 Android 签名，否则构建失败且不会发布 Release。普通 PR/main 检查仍可构建调试 APK。

## 发版

确认目标提交在 main 且 CI 通过后，在本地执行：

```sh
git pull --ff-only
git tag v0.1.0
git push origin v0.1.0
```

测试版本使用例如 `v0.2.0-rc.1`。不需要手动填写所有平台的版本。请根据实际版本选择 tag；上面只是示例，本次 CI 配置不会自动创建第一个产品版本。

工作流仅发布当前 tag 已存在的提交，测试/构建失败不发布。首次发布先创建草稿，文件全部上传后公开；失败后可以在 Actions 重跑，上传使用相同文件名覆盖。不要移动已对外发布的 tag。普通检查只有仓库读取权限，只有 tag 的发布 job 有 `contents: write`。

## 官网自动下载

官网 `/download` 在服务端读取与同源 `/api/releases` 共用的版本缓存；首屏直接包含安装包链接。缓存从 GitHub Release API 获取最新已发布且带客户端附件的版本，包括预览版本。按平台显示实际安装包 URL 和大小，文件直接从 GitHub 下载，不经 Worker 转发，也不需要发版后修改官网。

版本快照在 D1 永久保留，不设数据过期时间。有缓存就立即返回，只有首次没有缓存时同步读取 GitHub；距离上次 API 请求至少一小时才在后台异步刷新，并更新 D1、内存和边缘缓存。D1 同时保存 ETag。跨 Worker 实例通过 D1 短租约合并刷新，单实例合并并发请求，边缘缓存 5 分钟，浏览器缓存 1 分钟。GitHub 限流或故障时遵守 Retry-After / X-RateLimit-Reset 并继续提供上次成功的列表；空结果和冷启动失败也记录一小时的刷新间隔，不会由不同用户重复拉取。客户端不能指定仓库或强制刷新上游。

可选地为实际部署的 Worker 设置 `GITHUB_RELEASE_TOKEN` secret，提高 GitHub API 配额；公开仓库读取无需 token。token 仅在 Worker 使用，不进入浏览器或响应。下载目录仅在官网部署中启用，开源自部署不包含官网路由或下载缓存运行代码。

桌面安装包使用 NSGo 官方 `build.installers: true` 生成。Linux 构建环境安装 NSIS，确保 Windows 的安装 EXE 实际生成；缺失任何安装包都会阻止发版。CI 会挂载 DMG 验证应用和签名、检查 NSIS 内的程序，以及验证 DEB 的架构、GTK 依赖、桌面入口、Arch 包和 AppImage 标识。
