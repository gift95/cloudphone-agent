# scrcpy-server-port —— 本项目（cloudphone-v0.3.6）libsys_core 差异移植工程

> 基于 scrcpy v3.3.4 官方 `server/` Gradle 工程（Apache-2.0），移植本项目 `libsys_core.so`
> （`com.android.helper` fork）相对上游的核心差异，得到可继续二次开发的 Android 服务端工程。

## 一、差异总览（反编译对比数据）

| 项目 | 数值 |
| --- | --- |
| fork（libsys-core-java，反编译） | 154 个 .java |
| scrcpy v3.3.4 上游 server | 85 个 .java |
| fork 新增文件 | 70（其中约 50 个为反编译 lambda/枚举拆分产物，非真实新增） |
| 删除文件 | 1（`Server.java` → `CoreService.java`） |
| 同名修改文件 | 84（多数差异为 CFR 反编译噪声：`/* Decompiled */` 头、包名、变量名） |

完整对比证据：`C:\Users\Administrator\Documents\cloudphone-v0.3.6\_decomp\fork_vs_scrcpy_report.txt`、
`fork_vs_scrcpy_summary.json`。

## 二、真实逻辑差异与移植状态

### A. 已完成移植（本工程已落地，保持可编译）

| 差异 | 上游（scrcpy 3.3.4） | 本项目 fork | 移植位置 |
| --- | --- | --- | --- |
| 通道架构 | 3 通道（video/audio/control），LocalSocket 抽象名 | **4 通道**（+独立 touch 通道），SocketWrapper 抽象（LocalSocket / TCP 回环 `127.0.0.1:port`） | `device/DesktopConnection.java`（重写） |
| 控制通道注入 | `ControlChannel(LocalSocket)` | `ControlChannel(InputStream, OutputStream)` | `control/ControlChannel.java`（重写） |
| 入口类 | `Server` | `CoreService`（app_process 启动类改名） | `CoreService.java`（新增，逻辑与 Server 一致） |
| 连接参数 | `open(scid, tunnelForward, video, audio, control, sendDummyByte)` | `open(Options)`，Options 新增 **port**（TCP 回环基础端口） | `Server.java` 适配 + `Options.java` 注入 `--port` |

### B. 参考文件（已放入工程，默认不激活）

| 差异 | 说明 | 位置 |
| --- | --- | --- |
| AIDL 监听器 | fork 用 AIDL Binder（`IDisplayWindowListener` / `IOnPrimaryClipChangedListener`）替代上游 reflection，监听显示窗口/剪贴板变化 | `src/main/aidl/android/view/IDisplayWindowListener.aidl`、`src/main/aidl/android/content/IOnPrimaryClipChangedListener.aidl` |

启用：按文件头注释改造 `wrappers/DisplayManager.java`、`wrappers/ClipboardManager.java`。

### C. 需人工完成的差异（反编译产物不足以自动重建）

| 差异 | 说明 | 参考证据 | 状态 |
| --- | --- | --- | --- |
| Controller 触摸通道分流 | fork 的 `Controller` 将触摸事件走独立 touch 通道、剪贴板/控制命令走 control 通道 | 反编译 `Controller.java` + `DesktopConnection.getTouchChannel()` | ✅ 已完成（TouchChannel.java + Controller.setTouchChannel） |
| wrappers AIDL 化 | `ClipboardManager`/`DisplayManager`/`WindowManager`/`ActivityManager` 等 fork 改用了 AIDL/其他反射路径 | 反编译各 wrappers 文件 | 🔶 部分完成（AidlClipboardManager/AidlDisplayManager/AidlWrappers，WindowManager/ActivityManager 待补） |
| LogUtils 扩展 | fork 的 `util/LogUtils` 含编码器/显示/相机/应用列表构建 | 反编译 `LogUtils.java` | ⬜ 待完成 |
| CoreService.scrcpy() | fork 该方法反编译失败，本工程基于上游 `Server.scrcpy()` 重建 | 反编译 `CoreService.java` + 上游 `Server.java` | 🔶 基于上游重建，touch 分流 TODO |

### D. 编码格式扩展（H.265/AV1）

| 编码格式 | scrcpy 上游 | 本项目 fork | Agent 端 | 前端 | 状态 |
| --- | --- | --- | --- | --- | --- |
| H.264/AVC | ✅ 支持 | ✅ 支持 | ✅ Pion WebRTC | ✅ 所有浏览器 | ✅ 生产可用 |
| H.265/HEVC | ✅ v2.4+ | 🔶 需验证 MediaCodec | 🔶 MIME 已定义 | 🔶 Safari 17+/Chrome(flag) | 🔶 实验性 |
| AV1 | ✅ v3.0+ | 🔶 需验证 MediaCodec | 🔶 MIME 已定义 | 🔶 Chrome 70+/Firefox 67+ | 🔶 实验性 |

**编码格式选择**：Agent 通过 `-video-codec` 参数指定（h264/h265/av1/vp8/vp9），
scrcpy-server 通过 `video_codec` 参数接收（0=h264, 1=h265, 2=av1）。
推荐码率：AV1 可比 H.264 降低 ~45%，H.265 降低 ~30%。

**注意**：H.265/AV1 需要设备端 MediaCodec 硬件编码支持，且浏览器端需支持对应解码。
不支持时自动回退到 H.264。

## 三、构建方法（需 Android SDK）

```bash
# 环境要求：JDK 17+、Android SDK（compileSdk 36、build-tools、platforms;android-36）、AGP 8.13.0
# Windows：
set ANDROID_HOME=C:\path\to\Android\Sdk
gradlew.bat :server:assembleRelease
# Linux/macOS：
ANDROID_HOME=/path/to/sdk ./gradlew :server:assembleRelease
```

产物：`server/build/outputs/apk/release/server-release.apk`（未签名，push 到设备后以 app_process 运行，无需安装）。

## 四、部署与验证

```bash
# 将 APK push 到设备并解压运行（与项目 Agent 部署一致）：
adb push server-release.apk /data/local/tmp/
adb shell 'cd /data/local/tmp && unzip -o server-release.apk -d . && \
  app_process /system/bin com.genymobile.scrcpy.CoreService \
  --video --audio --control --port 8123 ...'
```

- TCP 四通道模式：`--port <base>`（v/a/t/c 依次 +0/+1/+2/+3）；
- LocalSocket 模式（兼容 scrcpy 原生）：`Server.java` 入口 + `--scid` 隧道。
- 验证：`adb logcat | grep scrcpy` 观察注册/推流日志。

## 五、许可证

- scrcpy：Apache-2.0（保留 LICENSE/NOTICE，注明修改）；
- 本项目 libsys_core fork 为对 scrcpy-server 的二次开发，本移植工程仅供学习与二次开发参考。

## 六、已知重建假设（诚实标注）

1. `DesktopConnection.open(Options, useTcp)` 为重建实现：fork 原方法反编译失败，
   端口偏移（+0/+1/+2/+3）与通道对应关系需对照实际 Agent（cloudphone-agent）的
   `uds_sys_v/a/t/c_` 转发配置核对；
2. `--port` 默认值 0（未配置），TCP 模式需显式传入；
3. touch 通道的 Controller 侧分流未实现（见 C 表）。
