# scrcpy-server 移植版

基于 [scrcpy](https://github.com/Genymobile/scrcpy) v3.3.4（Apache-2.0）的 Android 投屏服务端移植工程，针对 CloudPhone 项目进行了定制化修改。

## 与原版 scrcpy-server 的主要差异

### 1. 四通道 Unix Domain Socket (UDS) 物理隔离

原版 scrcpy-server 使用单个 TCP socket 传输视频、音频和控制消息。本移植版将其拆分为四个独立的 Unix Domain Socket：

| 通道 | Socket 名称 | 传输内容 | 优先级 |
|------|-------------|----------|--------|
| Video | `@scrcpy_video` | H.264 Annex B 裸流 | 高 |
| Audio | `@scrcpy_audio` | Opus 编码音频流 | 中 |
| Touch | `@scrcpy_touch` | 触控事件（高优先级） | 最高 |
| Control | `@scrcpy_control` | 其他控制指令（按键、滚动、剪贴板等） | 中 |

**优势**：即使在视频大码率瞬时拥塞的高负载状态下，触控指令仍通过独立的 Touch 通道瞬间送达并执行，杜绝操作指令排队等待。

### 2. 摄像头监控模式

在原版屏幕投屏基础上，新增了摄像头采集模式：

- 支持后置 / 前置摄像头切换
- 支持多种分辨率（1080p、2K、4K）
- 支持动态切换摄像头（通过控制消息 `TYPE_SWITCH_CAMERA=19`）
- 切换后自动请求关键帧，画面快速恢复

### 3. 控制消息扩展

新增控制消息类型：

| 类型 | 值 | 说明 |
|------|-----|------|
| `TYPE_SWITCH_CAMERA` | 19 | 切换前后置摄像头 |
| `TYPE_RESET_VIDEO` | 17 | 重置视频流并请求关键帧 |
| `TYPE_SET_BITRATE` | 18 | 动态调整编码码率 |
| `TYPE_START_APP` | 16 | 启动指定应用 |

### 4. 硬件级 PTS 时间戳直通

捕获 Android 硬件编码器（MediaCodec）输出的微秒级物理渲染时间戳（ptsUs），直接透传至 WebRTC RTP 包头，消除静态画面下的 Jitter Buffer 膨胀和首触卡顿。

### 5. 关键帧快速请求

通过 `MediaCodec.setParameters()` 主动请求关键帧，在切换摄像头、重启连接或画面异常时快速恢复，无需等待下一个 GOP 周期。

### 6. 日志时间戳

所有日志输出添加精确时间戳（`yyyy-MM-dd HH:mm:ss.SSS`），便于问题定位和性能分析。

### 7. APK 伪装防清理

编译产物打包为 APK 格式并改名为 `.so`（如 `libsys_core.so`），嵌入 Agent 二进制中，避免被 Android 系统的清理机制删除。

## 目录结构

```
scrcpy-server/
├── android/                          # Android 工程（Gradle）
│   ├── build.gradle                  # Gradle 构建配置
│   ├── proguard-rules.pro            # ProGuard 混淆规则
│   ├── meson.build                   # Meson 构建配置（可选）
│   ├── README-PORT.md                # 移植详细说明
│   ├── scripts/
│   │   └── build-wrapper.sh          # 构建包装脚本
│   └── src/main/
│       ├── AndroidManifest.xml       # Android 清单文件
│       ├── aidl/                     # AIDL 接口定义
│       │   └── android/
│       │       ├── content/
│       │       └── view/
│       └── java/com/genymobile/scrcpy/
│           ├── CoreService.java      # 核心服务入口
│           ├── Server.java            # 服务器主循环
│           ├── Options.java           # 配置选项
│           ├── video/                 # 视频采集与编码
│           │   ├── ScreenCapture.java      # 屏幕采集
│           │   ├── CameraCapture.java      # 摄像头采集（新增）
│           │   ├── NewDisplayCapture.java  # 虚拟显示采集
│           │   ├── SurfaceEncoder.java     # 表面编码器
│           │   ├── DisplaySizeMonitor.java # 显示尺寸监控
│           │   ├── VideoCodec.java         # 视频编解码器
│           │   ├── VideoFilter.java        # 视频滤镜
│           │   ├── VideoSource.java        # 视频源接口
│           │   ├── CameraAspectRatio.java  # 摄像头宽高比
│           │   ├── CameraFacing.java       # 摄像头朝向
│           │   └── CaptureReset.java       # 采集重置
│           ├── audio/                 # 音频采集与编码
│           │   ├── AudioCapture.java       # 音频采集接口
│           │   ├── AudioDirectCapture.java # 直接音频采集
│           │   ├── AudioPlaybackCapture.java # 播放音频采集
│           │   ├── AudioRawRecorder.java   # 原始音频录制
│           │   ├── AudioRecordReader.java  # 音频读取器
│           │   ├── AudioEncoder.java       # 音频编码器
│           │   ├── AudioCodec.java         # 音频编解码器
│           │   ├── AudioConfig.java        # 音频配置
│           │   ├── AudioSource.java        # 音频源
│           │   └── AsyncProcessor.java     # 异步处理器
│           ├── control/               # 控制消息处理
│           │   ├── Controller.java          # 控制器
│           │   ├── ControlMessage.java      # 控制消息定义
│           │   ├── ControlMessageReader.java # 控制消息读取器
│           │   ├── ControlChannel.java      # 控制通道
│           │   ├── KeyComposition.java      # 按键组合
│           │   ├── Pointer.java             # 指针
│           │   ├── PointersState.java       # 指针状态
│           │   ├── PositionMapper.java      # 位置映射
│           │   └── UhidManager.java         # UHID 管理
│           ├── device/                # 设备管理
│           │   ├── DesktopConnection.java   # 桌面连接（UDS 四通道）
│           │   ├── Device.java              # 设备信息
│           │   ├── DeviceApp.java           # 设备应用
│           │   ├── DisplayInfo.java         # 显示信息
│           │   ├── NewDisplay.java          # 新显示
│           │   ├── Orientation.java         # 屏幕方向
│           │   ├── Point.java               # 点
│           │   ├── Position.java            # 位置
│           │   ├── Size.java                # 尺寸
│           │   ├── Streamer.java            # 流传输器
│           │   ├── DeviceMessage.java       # 设备消息
│           │   ├── DeviceMessageSender.java # 设备消息发送器
│           │   └── DeviceMessageWriter.java # 设备消息写入器
│           ├── opengl/                # OpenGL 滤镜
│           │   ├── AffineOpenGLFilter.java  # 仿射变换滤镜
│           │   ├── AffineMatrix.java         # 仿射矩阵
│           │   ├── GLUtils.java              # OpenGL 工具
│           │   ├── OpenGLException.java      # OpenGL 异常
│           │   ├── OpenGLFilter.java         # OpenGL 滤镜接口
│           │   └── OpenGLRunner.java         # OpenGL 运行器
│           ├── wrappers/              # Android API 包装
│           │   ├── ActivityManager.java      # Activity 管理器
│           │   ├── ClipboardManager.java     # 剪贴板管理器
│           │   ├── ContentProvider.java      # 内容提供者
│           │   ├── DisplayControl.java       # 显示控制
│           │   ├── DisplayManager.java       # 显示管理器
│           │   ├── DisplayWindowListener.java # 显示窗口监听器
│           │   ├── InputManager.java         # 输入管理器
│           │   ├── PowerManager.java         # 电源管理器
│           │   ├── ServiceManager.java       # 服务管理器
│           │   ├── StatusBarManager.java     # 状态栏管理器
│           │   ├── SurfaceControl.java       # 表面控制
│           │   ├── WindowManager.java        # 窗口管理器
│           │   └── FakeContext.java          # 伪造 Context
│           └── util/                  # 工具类
│               ├── Binary.java              # 二进制工具
│               ├── Codec.java               # 编解码器工具
│               ├── CodecOption.java         # 编解码器选项
│               ├── CodecUtils.java          # 编解码器工具
│               ├── Command.java             # 命令执行
│               ├── HandlerExecutor.java     # Handler 执行器
│               ├── IO.java                  # IO 工具
│               ├── Ln.java                  # 日志（带时间戳）
│               ├── LogUtils.java            # 日志工具
│               ├── Settings.java            # 设置
│               ├── SettingsException.java   # 设置异常
│               ├── StringUtils.java         # 字符串工具
│               ├── Threads.java             # 线程工具
│               ├── CleanUp.java             # 清理工具
│               ├── Workarounds.java         # 兼容性处理
│               └── AndroidVersions.java     # Android 版本
└── README.md                        # 本文件
```

## 编译

### 方式一：Gradle（推荐）

```bash
cd android

# 需要 JDK 17+ 和 Android SDK（compileSdk 36）
./gradlew :server:assembleRelease
```

编译产物：`android/server/build/outputs/apk/release/server-release.apk`

### 方式二：无 Gradle 手动编译

参考 `android/build_without_gradle.sh`，使用 `javac` + `dx` / `d8` 手动编译。

```bash
cd android
./build_without_gradle.sh
```

### 方式三：Meson（可选）

```bash
cd android
meson setup build
ninja -C build
```

## 部署

编译完成后，将 APK 改名为 `.so` 并嵌入 Agent：

```bash
# 1. 重命名 APK
cp server-release.apk libsys_core.so

# 2. 复制到 Agent 源码目录
cp libsys_core.so ../../agent/

# 3. 重新编译 Agent（会自动嵌入 libsys_core.so）
cd ../../agent
GOOS=android GOARCH=arm64 go build -o cloudphone-agent-arm64 .
```

## 控制消息协议

### 消息格式

```
+----------------+----------------+----------------+
|  type (1 byte) |  payload (variable)            |
+----------------+----------------+----------------+
```

### 消息类型

| 类型 | 值 | Payload 格式 | 说明 |
|------|-----|-------------|------|
| `TYPE_INJECT_KEYCODE` | 0 | action(1) + keycode(4) + repeat(4) + metastate(4) | 注入按键事件 |
| `TYPE_INJECT_TEXT` | 1 | length(4) + text(length) | 注入文字 |
| `TYPE_INJECT_TOUCH_EVENT` | 2 | action(1) + pointerId(8) + x(4) + y(4) + pressure(2) + buttons(4) | 注入触控事件 |
| `TYPE_INJECT_SCROLL_EVENT` | 3 | x(4) + y(4) + hScroll(4) + vScroll(4) + buttons(4) | 注入滚动事件 |
| `TYPE_BACK_OR_SCREEN_ON` | 4 | action(1) | 返回或亮屏 |
| `TYPE_EXPAND_NOTIFICATION` | 5 | - | 展开通知栏 |
| `TYPE_EXPAND_SETTINGS` | 6 | - | 展开设置面板 |
| `TYPE_COLLAPSE_PANELS` | 7 | - | 收起面板 |
| `TYPE_GET_CLIPBOARD` | 8 | - | 获取剪贴板 |
| `TYPE_SET_CLIPBOARD` | 9 | sequence(8) + paste(1) + length(4) + text(length) | 设置剪贴板 |
| `TYPE_SET_DISPLAY_POWER` | 10 | mode(1) | 设置显示电源 |
| `TYPE_ROTATE_DEVICE` | 11 | - | 旋转设备 |
| `TYPE_UHID_CREATE` | 12 | id(2) + type(2) + ... | 创建 UHID 设备 |
| `TYPE_UHID_INPUT` | 13 | id(2) + length(2) + data(length) | UHID 输入 |
| `TYPE_UHID_DESTROY` | 14 | id(2) | 销毁 UHID 设备 |
| `TYPE_OPEN_HARD_KEYBOARD` | 15 | - | 打开硬键盘 |
| `TYPE_START_APP` | 16 | length(4) + package(length) | 启动应用 |
| `TYPE_RESET_VIDEO` | 17 | - | 重置视频并请求关键帧 |
| `TYPE_SET_BITRATE` | 18 | bitrate(8) | 设置编码码率 |
| `TYPE_SWITCH_CAMERA` | 19 | cameraId(1) | 切换摄像头 |

## 与 Agent 的 UDS 通信协议

### Video 通道

```
+----------------+----------------+----------------+----------------+
|  pts (8 bytes) |  config(1 byte)|  size(4 bytes) |  data(size)   |
+----------------+----------------+----------------+----------------+
```

- `pts`：微秒级时间戳
- `config`：是否为 SPS/PPS 配置帧
- `size`：H.264 NAL 单元大小
- `data`：H.264 Annex B 数据

### Audio 通道

```
+----------------+----------------+----------------+
|  pts (8 bytes) |  size(4 bytes) |  data(size)   |
+----------------+----------------+----------------+
```

- `pts`：微秒级时间戳
- `size`：Opus 帧大小
- `data`：Opus 编码数据

### Touch / Control 通道

直接传输控制消息二进制数据（见上方控制消息协议）。

## 摄像头 ID 映射

| cameraId | 说明 |
|----------|------|
| 0 | 后置摄像头（默认） |
| 1 | 前置摄像头 |

> 注意：不同设备的摄像头 ID 可能不同，建议通过 `CameraManager.getCameraIdList()` 查询。

## 性能优化建议

1. **码率设置**：局域网环境建议 4-8Mbps，互联网环境建议 2-4Mbps
2. **分辨率**：默认 1080p，高性能设备可使用 2K/4K
3. **帧率**：默认 30fps，静态画面可降低至 15fps 节省带宽
4. **关键帧间隔**：建议 2-5 秒，平衡延迟和容错
5. **音频**：Opus 48kHz 立体声，码率 64-128kbps

## 已知限制

1. 部分设备的 MediaCodec 不支持动态调整码率，需要重启编码器
2. 摄像头模式下部分设备不支持 4K 分辨率
3. 音频采集需要 Android 10+（`AudioPlaybackCapture` API）
4. UHID 功能需要 root 权限或系统签名

## 致谢

本项目基于 [Genymobile/scrcpy](https://github.com/Genymobile/scrcpy) v3.3.4（Apache-2.0）进行二次开发，保留了上游的核心架构和优秀设计，并针对 CloudPhone 项目的需求进行了定制化扩展。

## 许可证

本项目基于 Apache License 2.0 开源，修改部分以 MIT 许可证发布。详见各源文件头部的许可证声明。
