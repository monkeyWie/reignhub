# ReignHub · NSGo 客户端

桌面、移动端与浏览器共用此 Go UI 实现，复用根模块 Go 后端。原生端支持 WebRTC 与 TCP P2P，浏览器使用 WebRTC，均可回退到加密中继。

## 技术与目录

按照 [NSGo 官方文档](https://nsgo.dev/zh/docs/) 开发，依赖版本以本目录 [go.mod](go.mod) 和 `go.sum` 为准。消息选区在 macOS、iOS、Android 和 Web 使用系统或浏览器的文本菜单；Windows 和 Linux 右键提供“复制”和“全选”，“全选”作用于当前消息正文。升级 NSGo 时应做平台和界面回归。

- 原生平台使用 NSGo 平台视图，无 WebView；浏览器使用官方 Go WASM + DOM 后端。
- `hubui/`：统一设备与会话侧栏、本机 Agent 面板、可配置项目树、聊天、模型选择、授权、图片附件及响应式布局。
- `hubui/api_native.go`：在同一个应用进程内直接调用已有 `internal/bridge`，无需 FFI、CLI 包装器或独立 Hub 后台进程。
- `webapi/`：迁移原浏览器前端负责的配对、保存设备、离线快照、目录增量同步及 IndexedDB 附件缓存。
- `browserclient/`：与已有 Go 主机兼容的加密协议和浏览器 RTCPeerConnection；优先尝试直连，无法直连时走现有中继。无 TURN、DERP、Tailcat。
- `testsupport/fixture/`：仅限本机的隔离网络测试主机，不访问真实 Agent。
- 根目录 `scripts/build.sh`、`scripts/go-toolexec.sh`、`scripts/build-android-host.py`：统一构建入口、链接参数与 Android 宿主打包。
- `cmd/android-package/`：Android manifest 与 APK 的 Go 打包工具，使用本模块的 NSGo 依赖。

本模块通过 `replace github.com/monkeyWie/reignhub => ../..` 复用后端；两个 Go module 分别运行测试。

## 开发和启动

需要 Go 和相应平台工具，先用官方检查命令确认：

```sh
cd clients/nsgo
GOWORK=off go tool nsgo doctor
GOWORK=off go tool nsgo dev
```

隔离演示（不会扫描、启动或控制真实 Agent）：

```sh
AGENTHUB_NSGO_DEMO=1 GOWORK=off go tool nsgo dev
```

也可以直接启动已构建的 macOS 包：

```sh
'../../dist/ReignHub-0.1.0-macos_arm64/ReignHub.app/Contents/MacOS/ReignHub'
```

NSGo 应用 ID 是 `dev.agenthub.nsgo`，设备身份和缓存保存在该应用的数据目录。开发时可用 `AGENTHUB_NSGO_DATA_DIR` 指定独立测试目录。

原生客户端默认服务为 `https://reignhub.app`。可以在界面里改为自部署服务，或在第一次启动前设置运行环境变量 `AGENTHUB_SERVER_URL`。改变默认构建值（以下构建命令从仓库根目录执行）：

```sh
AGENTHUB_SERVER_URL=https://example.com ./scripts/build.sh macos_arm64,android_arm64
```

桌面、手机和 Web 都支持 HTTP/WS 与 HTTPS/WSS，可直接使用公网 IP，无需 LAN 编译例外。例如：

```sh
AGENTHUB_SERVER_URL=http://192.168.1.10:26330 ./scripts/build.sh macos_arm64,web
```

已有设备配置不会被构建默认值覆盖，仍需在界面修改。Web 按当前 origin 连接；HTTP 下部分浏览器能力受安全上下文限制，WebRTC 不可用时保留 WS 中继。公网 Web 推荐 HTTPS，但 TLS 不是连接的强制条件。

浏览器始终使用当前页面的 `location.origin` 作为服务地址。正常生产访问必须在同一 origin 部署静态页面与 `/p2p/*`；`?demo=1` 只打开隔离演示，不是连接真实设备的入口。

## 构建和分发

从仓库根目录执行，脚本自动进入 NSGo module：

```sh
./scripts/build.sh
# 或选择目标
./scripts/build.sh macos_arm64,windows_x64,linux_x64,ios,ios_simulator,android_arm64,web
```

目标与名字由 `nsgo.yaml` 配置，使用官方 `go tool nsgo build`。默认输出在仓库根目录 `dist/`，APK、IPA、Web ZIP 在根目录 `dist/releases/`，每个发布文件带 SHA-256。显式 `-output` 支持绝对路径，相对路径按调用者当前目录解析；仓库路径包含空格也可构建。

Android 构建中，现有 Pion 依赖 `anet` 引用 Go 私有 zone cache，需要 `-checklinkname=0`。包装脚本仅在 Android 链接时追加这个标志，不修改 Go 安装、依赖缓存或后端源码。

- Android APK 支持通过 CI Secret `ANDROID_SIGNING_PEM_BASE64` 配置固定 PEM 签名身份；未配置时使用临时调试签名。详见 [CI 与 Release](../../docs/ci-and-releases.md)。
- iOS IPA 已构建但未签名；安装到真机、TestFlight 或发布需要 Apple 签名配置。

### Android 后台连接与通知

Android 通过 NSGo 的公开 Host API 调用 `ConnectionService`。有连接或用户发起的连接尝试时启用 `connectedDevice` 前台服务，通知栏显示连接状态；没有连接时停止服务。所有端切后台后都继续同步，前台服务启动失败或停止也不会主动取消 Go 订阅和网络连接。系统实际挂起进程或中断连接后，恢复运行时自动补同步；回到前台不会重建仍健康的连接，也不会重发消息。

实时观察到的任务按设备、Provider、会话和 turn 隔离通知；运行中显示进度，完成、失败或停止会更新通知。切到其他会话后，独立的 watch 继续跟踪已观察到的运行任务；历史缓存不会触发通知。点击任务通知重新打开对应会话。活动任务在线时持有带超时、定期续期的 partial WakeLock，结束、离线或服务停止时释放。

构建原生 Host 需要 Android SDK 的 `aapt2`、`d8`、`zipalign`、`apksigner` 和 OpenSSL；manifest 交给 SDK 编译，保留服务类型及权限。CI 仍使用已有签名身份。服务不会在用户移除应用任务后自行复活。Android 厂商省电和强制停止仍可能结束进程，iOS 和浏览器不采用 Android 的常驻服务方案。

- Windows、Linux 包已交叉构建，尚未在对应真机运行。

开发用 `reignhub-server` 可内嵌 ReignHub Web，执行 `./reignhub-server` 即可使用。`--web-enabled=false` 可关闭页面。以下外部目录方式仅用于开发时覆盖内嵌资源：

```sh
reignhub-server -listen :26330 -stun-listen :26331 -tcp-stun-listen :26331 \
  -state ./data/reignhub.db \
  -origins https://reignhub.app \
  -web '/path/to/assets/chat'
```

HTTP/WS 可直接访问，也可用 Nginx 提供 HTTPS/WSS 并反代 26330。TCP 和 UDP STUN 的 26331 端口单独直达，不走普通 HTTP 反代。

打包 Linux、macOS 和 Windows 服务（包含 NSGo Web，保留当前 LAN 产物和服务）：

```sh
python3 scripts/platform/package-server.py --version X.Y.Z
```

输出在仓库根目录 `dist/release/`，有三个平台和 amd64 / arm64 两种架构，Web 已内嵌在 `reignhub-server`，包含 Nginx、systemd 示例和 SHA256 清单，不需要启动脚本或外部 Web 目录。客户端填写 `http://公网IP:26330`，或配置反代后填写 `https://reignhub.app`。TCP/UDP 26331 均单独直达。详见 [服务部署说明](deploy/service/README.md)。

## 界面和同步

采用 Zinc 中性色与绿色品牌强调。单一侧栏顶部管理本机、远程设备和添加设备；进入即加载所有环境的会话。650 点以下保持单页工作区，通过左上角菜单打开侧栏抽屉，选中会话后自动收起；开关抽屉保留当前消息、阅读位置、输入草稿和会话同步。650 点及以上用可拖动的 300 点侧栏。

置顶、最近是平铺列表：最近位于项目下方，按发送消息或 Agent 回复完成时间倒序展示所有符合筛选条件的未置顶会话，不限制条数，包含已完成和离线缓存的会话；收起项目后即可直接浏览最近列表。活动时间未知时使用创建时间，两者均未知时排在最后。置顶和最近分区每次启动默认展开，仍可手动收起。项目列表中的未置顶会话按创建时间倒序，支持项目（默认）、设备 → 项目、设备 → Agent → 项目三级分组规则和逐级折叠。相同仓库项目可跨设备聚合，不同设备上的同名目录不会误合并；每个会话展示设备和 Agent。未知创建时间排在最后，不用最后更新时间替代。分组、置顶和其他分区的展开状态在本地保存。

项目与最近标题右侧各有独立的筛选入口，桌面 hover 或键盘聚焦时显示，触屏默认显示；打开面板或已启用筛选时保持可见。两者均支持设备、Agent、项目多选，只有项目面板提供分组方式；置顶不受筛选影响。同维度取并集，不同维度取交集，默认包含全部，各自支持一键清除。筛选不切换当前对话、不丢失草稿，重启后恢复全部。顶层入口依次为置顶、项目、最近；没有置顶时自动隐藏该分区。置顶会话只在置顶分区显示，从最近和项目树中移出；置顶时自动展开该分区，取消置顶后恢复到原项目并重新参与最近排序。项目树不展示没有未置顶会话的空分组。一级入口折叠时常驻显示向右的三角箭头，展开时在桌面 hover 或键盘聚焦后显示向下箭头；置顶和移除按钮同样在 hover 或键盘聚焦后显示，右键菜单提供同样操作；触屏长按打开菜单。移除前会说明对应 Agent 的处理方式，执行中的会话不能移除。悬停详情使用有边框和背景的卡片。

本机通过进程内 Caller 与远程共用 Dispatcher、缓存和同步逻辑，无须联网或配对；远程只增加鉴权和加密传输层。浏览器和手机聚合远程设备。新建会话位于侧栏底部，明确选择执行设备、Agent 和该设备上的工作目录。底部设置入口在未登录时只显示图标，已登录时增加用户名；点击打开账号、设置、主题、刷新及帮助面板。

聊天使用 NSGo 原生虚拟列表与增量 Markdown。通过现有 watch 协议接收文本增量；重连、事件缺口及快照通知会补最新状态。草稿、所选模型和上传内容按会话隔离，目录刷新不重建输入框。历史中被新一轮取代的 inProgress 不会让 Thinking 常驻。

输入框内部放模型和发送操作；桌面 / Web 的 Enter 发送，Shift+Enter 换行，手机用发送按钮。图片使用缩略图和可切换的放大预览，文件支持下载；单个文件最多 50 MB、单条消息最多 16 个附件和 100 MB。浏览器附件保存在 IndexedDB，按最近使用淘汰，原始附件预算 96 MB；实际序列化占用另有开销。原生客户端复用后端磁盘缓存。

生成中的助手消息不显示复制 footer；Thinking 只在最初没有公开文本或工具活动时显示；工具活动默认折叠，流光使用官方 `ui/chat.Shimmer`，完成后移除临时活动。Markdown 的跨段落选择使用官方 TextView。

## 验证

常用测试：

```sh
GOWORK=off go test ./...
GOWORK=off go test -race ./browserclient ./webapi ./hubui
GOWORK=off go tool nsgo test -target macos -run 'TestDesktopNavigationAndDrafts|TestLiveStatusAndEditorStability' ./hubui
GOWORK=off go tool nsgo test -target web -run 'TestPhoneNavigation|TestComposerSubmit' ./hubui
GOWORK=off go tool nsgo test -target ios_simulator -run TestPhoneNavigation ./hubui
GOWORK=off go tool nsgo test -target web -run TestBrowserIndexedDBPersistence ./webapi
```

浏览器网络互通验证（两个终端，第二个 URL 取第一个输出）：

```sh
GOWORK=off go run ./testsupport/fixture
GOWORK=off go tool nsgo test -target web \
  -env AGENTHUB_NSGO_TEST_FIXTURE=http://127.0.0.1:PORT \
  -run TestBrowserP2PAndRelayWithExistingHost ./browserclient
```

## 平台验证范围

- 实时扫码使用 NSGo 官方 ScanCode 接口调用 Android 和 iOS 相机，照片不上传到服务器；权限和前后台恢复需要对应平台验证。
- 尚无 Android / iOS 真机验收，也未测试公网真实设备配对；不把交叉编译、模拟器或隔离测试当作这些验证。
- 原生字体、Markdown 内部排版及弹窗仍由 NSGo 原生组件绘制。各平台的文本选择、输入、滚动和窗口行为需分别验收，不能用单一平台的截图代替全部平台检查。

参考：[入门](https://nsgo.dev/zh/docs/getting-started/)、[构建](https://nsgo.dev/zh/docs/building/)、[列表](https://nsgo.dev/zh/docs/lists/)、[文本](https://nsgo.dev/zh/docs/text/)、[测试](https://nsgo.dev/zh/docs/testing/)；API 同时按固定版本官方源码核对。
