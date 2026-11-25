# ReignHub 原生能力扩展

按 NSGo 官方公开 API 接入，不使用 WebView，不修改 NSGo 模块缓存。

- 官方导航：https://nsgo.dev/zh/docs/navigation/
- 官方原生组件与宿主通道：https://nsgo.dev/zh/docs/embedding/
- 官方平台服务：https://nsgo.dev/zh/docs/services/

## Pairing and mobile hosts

The controller requests QR codes through NSGo's `core.Scope.ScanCode`. Android
declares CAMERA in its manifest and iOS supplies NSCameraUsageDescription.
A valid scan fills the connection form. Pairing still checks the remote key
and follows the existing authorization flow. Users can retry the camera, select
an image or enter a device ID. The gallery decodes the selected image locally.

Android's RasActivity extends the official NsgoActivity for device metadata,
background connections and screen-awake control. Build with the root
`scripts/build.sh android_arm64` entry. The Android host packaging step compiles
the app's Java sources against the official AAR, repackages its activity and
services and signs the APK with the configured build identity.

On iOS the native host channel provides the device name. The system may return
a generic iPhone or iPad name.

## Conversation display lifetime

`ras.screen-awake` accepts `{enabled: boolean}`. Android's activity applies
`FLAG_KEEP_SCREEN_ON` only while resumed, clears it on pause/destroy and reapplies
the current request on resume. It does not acquire a CPU wake lock or require a
new permission. The controller releases its request when leaving the visible
conversation, including a native Back/swipe pop. Background-service retention
does not keep the display awake. The pinned NSGo Android KeepAwake backend is a
no-op, so this uses the existing public host bridge. iOS uses NSGo's display
KeepAwake hold with the same page/lifecycle policy. Mobile Web uses Screen Wake
Lock where supported over HTTPS or localhost; plain LAN HTTP cannot provide it.

Lifecycle regressions live in `hubui/screen_awake_test.go`; browser tests cover
late request completion after cancellation/disposal and interaction-based
recovery after denial. Physical-device screen timeout/lock behavior still needs
device verification; users can always lock the display explicitly.
