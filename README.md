# CloudPhone

基于 WebRTC 的云手机 / 安卓远程控制解决方案。自研 Go Agent + 移植版 scrcpy-server，支持低延迟投屏、触控回传、音频透传、文件管理、批量群控、摄像头监控等功能。

## ✨ 特性

### 核心功能
- 📱 **WebRTC 投屏**：H.264 硬件编码，低延迟视频流，支持 P2P 直连与 TURN 中继降级
- 🖱️ **触控回传**：点击、滑动、滚动、按键事件、文字输入、剪贴板双向同步
- 🔊 **音频透传**：Opus 编码音频实时传输，支持独立开关控制
- 📁 **文件管理**：WebRTC DataChannel 高速文件传输，支持上传下载与批量操作
- 👥 **批量群控**：多设备同步操作、批量静默安装 APK、批量打开应用
- 🎥 **摄像头监控**：前后置摄像头实时预览，支持分辨率切换与 4K 编码
- 🖥️ **WebSocket 投屏**：备用投屏方案，兼容不支持 WebRTC 的浏览器
- ⌨️ **ADB 交互式终端**：WebRTC DataChannel PTY，支持完整 Shell 交互

### 技术亮点
- 🔧 **UDS 三通道物理隔离**：video / audio / touch / control 四个独立 Unix Domain Socket，高负载下触控指令零排队
- ⏱️ **硬件级 PTS 时间戳直通**：MediaCodec 输出的微秒级渲染时间戳直接透传至 RTP 包头，消除首触卡顿
- 📈 **动态码率调整（BWE）**：基于网络带宽估计实时调整编码码率，2Mbps ~ 8Mbps 自适应
- 🎯 **Keyframe 快速恢复**：切换摄像头 / 重启后强制请求关键帧，画面出现时间从 ~19s 优化到 ~1-2s
- 🔒 **APK 伪装防清理**：scrcpy-server 打包为 APK 改名为 .so，嵌入 Agent 二进制，避免被 Android 系统清理

## 🏗️ 架构

```
┌─────────────────────────────────────────────────────────────┐
│                     浏览器 (Vue 3 + WebRTC)                  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │ 视频播放  │  │ 触控回传  │  │ 文件管理  │  │ ADB 终端  │  │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘  │
└──────────────────────────────┬──────────────────────────────┘
                               │ WebSocket 信令 + WebRTC P2P/TURN
┌──────────────────────────────▼──────────────────────────────┐
│                  信令服务器 (Go + Gorilla WebSocket)          │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │ 鉴权管理  │  │ Hub 调度  │  │ REST API  │  │ 静态资源  │  │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘  │
└──────────────────────────────┬──────────────────────────────┘
                               │ WebRTC P2P / TURN
┌──────────────────────────────▼──────────────────────────────┐
│               Agent (Go + Pion WebRTC + UDS)                 │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │ WebRTC   │  │ 信令处理  │  │ 触控注入  │  │ 文件传输  │  │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘  │
└──────────────────────────────┬──────────────────────────────┘
                               │ Unix Domain Socket (4 通道)
┌──────────────────────────────▼──────────────────────────────┐
│            scrcpy-server (Java + Android MediaCodec)         │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │ 视频编码  │  │ 音频编码  │  │ 触控处理  │  │ 摄像头   │  │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘  │
└──────────────────────────────┬──────────────────────────────┘
                               │ Android Framework API
┌──────────────────────────────▼──────────────────────────────┐
│                        Android 设备                            │
└─────────────────────────────────────────────────────────────┘
```

## 📁 目录结构

```
cloudphone/
├── agent/                  # 自研 Go Agent（端侧控制程序）
│   ├── main.go             # 入口，参数解析
│   ├── config.go           # 配置管理
│   ├── signaling.go        # WebSocket 信令客户端
│   ├── webrtc.go           # Pion WebRTC 连接管理
│   ├── scrcpy.go           # scrcpy-server 进程管理与 UDS 通信
│   ├── control.go          # 控制消息处理（触控/按键/滚动等）
│   ├── metrics.go          # 性能指标统计
│   ├── embed.go            # 嵌入 scrcpy-server APK
│   ├── go.mod
│   └── go.sum
│
├── server/                 # 信令服务器（Go）
│   ├── main.go             # 入口，路由注册，静态资源服务
│   ├── auth.go             # 登录鉴权，用户管理，token 验证
│   ├── hub.go              # WebSocket Hub，Agent/Client 调度
│   ├── rest_api.go         # REST API 端点
│   ├── license.go          # 许可证管理（已移除限制）
│   ├── go.mod
│   └── go.sum
│
├── frontend/               # 前端（Vue 3 + Vite）
│   ├── src/
│   │   ├── views/          # 页面组件（设备列表、设备控制、群控等）
│   │   ├── components/     # 可复用组件
│   │   ├── composables/    # 组合式函数（WebRTC、ADB、部署等）
│   │   ├── stores/         # Pinia 状态管理
│   │   ├── utils/          # 工具函数
│   │   ├── styles/         # 全局样式
│   │   ├── router.js       # 路由配置
│   │   └── main.js         # 应用入口
│   ├── public/             # 静态资源
│   ├── index.html
│   ├── package.json
│   └── vite.config.js
│
├── scrcpy-server/          # 移植版 scrcpy-server（Java/Android）
│   ├── android/            # Android 工程（Gradle）
│   │   └── src/main/java/com/genymobile/scrcpy/
│   │       ├── video/      # 视频采集与编码（Display/Camera）
│   │       ├── audio/      # 音频采集与编码
│   │       ├── control/    # 控制消息处理
│   │       ├── device/     # 设备管理（DesktopConnection 等）
│   │       ├── opengl/     # OpenGL 滤镜
│   │       ├── wrappers/   # Android API 包装
│   │       └── util/       # 工具类
│   └── README-PORT.md      # 移植说明
│
├── android-app/            # Agent 安卓应用（APK 包装器）
│   ├── app/
│   │   └── src/main/
│   │       ├── assets/     # 嵌入的 cloudphone-agent-arm64 + libsys_core.so
│   │       ├── java/com/cloudphone/agent/
│   │       │   ├── MainActivity.java      # 配置界面
│   │       │   ├── AgentBootstrap.java    # Agent 启动引导
│   │       │   ├── AgentConfig.java       # 配置管理
│   │       │   ├── AgentService.java      # 前台服务
│   │       │   ├── BootReceiver.java      # 开机自启
│   │       │   └── ShizukuLauncher.java  # Shizuku 权限封装
│   │       └── AndroidManifest.xml
│   ├── build.gradle
│   └── README.md
│
├── .gitignore
├── README.md
└── LICENSE
```

## 🚀 快速开始

### 环境要求

- **Go** 1.22+（Agent 和信令服务器）
- **Node.js** 18+（前端）
- **JDK** 17+（scrcpy-server，可选）
- **Android SDK**（scrcpy-server，可选）
- **Android 设备**（Android 10+，支持 ADB 调试）

### 🐳 方式一：Docker 一键部署（推荐）

```bash
# 1. 克隆项目
git clone https://github.com/gift95/cloudphone.git
cd cloudphone

# 2. 修改 docker-compose.yml 中的 PUBLIC_IP 为你的公网 IP

# 3. 构建并启动（包含信令服务器 + TURN 中继）
docker-compose up -d

# 4. 访问 https://你的IP:8443（默认账号 admin/admin123）
```

详细配置请参考 [Docker 部署指南](docker/README.md)。

### 🔧 方式二：手动编译部署

### 1. 编译信令服务器

```bash
cd server
go mod tidy
go build -o cloudphone-sign .
```

### 2. 编译前端

```bash
cd frontend
npm install
npm run build
```

编译产物在 `frontend/dist/` 目录。

### 3. 编译 Agent

```bash
cd agent
go mod tidy

# Linux 本地编译（测试用）
go build -o cloudphone-agent .

# Android 交叉编译（部署到设备）
GOOS=android GOARCH=arm64 go build -o cloudphone-agent-arm64 .
```

> **注意**：Agent 编译时会通过 `embed.go` 自动嵌入同目录下的 `libsys_core.so`（scrcpy-server APK 改名）。请确保该文件存在。

### 4. 编译 scrcpy-server（可选）

如果需要修改 scrcpy-server 源码，参考 `scrcpy-server/android/README-PORT.md`。

预编译的 `libsys_core.so` 已嵌入 Agent，无需单独编译。

### 5. 部署与运行

#### 服务端

```bash
# 启动信令服务器（HTTP 模式，内网调试）
./cloudphone-sign \
  -port 8443 \
  -host 0.0.0.0 \
  -tls=false \
  -assets /path/to/frontend/dist \
  -data /path/to/data
```

默认管理员账号：`admin / admin123`（首次登录后请修改密码）。

#### 设备端

```bash
# 推送 Agent 到设备
adb push cloudphone-agent-arm64 /data/local/tmp/
adb shell chmod +x /data/local/tmp/cloudphone-agent-arm64

# 启动 Agent
adb shell "cd /data/local/tmp && \
  nohup ./cloudphone-agent-arm64 \
    -signaling ws://<服务器IP>:8443/register_agent \
    -id device-001 \
    -server-port 8133 \
    -bitrate 2000000 \
    -audio=true \
    -debug \
    >/data/local/tmp/agent.log 2>&1 &"
```

#### 访问

在浏览器中打开 `http://<服务器IP>:8443/`，登录后即可看到设备列表并进行控制。

## ⚙️ 配置参数

### 信令服务器

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-port` | 8443 | 监听端口 |
| `-host` | 0.0.0.0 | 监听地址 |
| `-tls` | true | 是否启用 HTTPS |
| `-cert` | certs/server.crt | SSL 证书路径 |
| `-key` | certs/server.key | SSL 私钥路径 |
| `-assets` | ./assets | 前端静态资源目录 |
| `-data` | ./data | 持久化数据目录 |
| `-no-auth` | false | 关闭登录鉴权（仅内网调试） |
| `-ice_servers` | 公共 STUN | 自定义 STUN/TURN 服务器 |

### Agent

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-signaling` | - | 信令服务器 WebSocket 地址（必填） |
| `-id` | - | 设备唯一标识（必填） |
| `-jar` | /data/local/tmp/libsys_core.so | scrcpy-server 路径 |
| `-server-port` | 8133 | scrcpy-server 监听端口 |
| `-bitrate` | 2000000 | 初始视频码率（bps） |
| `-audio` | true | 是否启用音频 |
| `-debug` | false | 是否启用调试日志 |

## 🛠️ 技术栈

| 组件 | 技术 |
|------|------|
| **前端** | Vue 3 + Vite + Pinia + WebRTC + xterm.js |
| **信令服务器** | Go + Gorilla WebSocket + Pion WebRTC |
| **Agent** | Go + Pion WebRTC + Unix Domain Socket |
| **scrcpy-server** | Java + Android MediaCodec + Camera2 + AudioRecord |
| **视频编码** | H.264 (Hardware / Baseline Profile) |
| **音频编码** | Opus |
| **传输协议** | WebRTC (SRTP/DTLS) + WebSocket 信令 |

## 📊 性能指标

在局域网环境（千兆以太网，Android 13 设备）下的实测数据：

| 指标 | 数值 |
|------|------|
| 视频延迟 | 30-80ms |
| 帧率 | 30-60 FPS |
| 码率范围 | 2-8 Mbps（动态调整） |
| 触控延迟 | <10ms |
| 连接建立时间 | 1-3s |
| 摄像头切换恢复 | 1-2s（Keyframe 快速恢复） |

## 🤝 致谢

本项目基于以下开源项目：

- [scrcpy](https://github.com/Genymobile/scrcpy) - 屏幕镜像与控制（Apache-2.0）
- [Pion WebRTC](https://github.com/pion/webrtc) - Go 语言 WebRTC 实现（MIT）
- [Gorilla WebSocket](https://github.com/gorilla/websocket) - Go WebSocket 库（BSD-2-Clause）
- [Vue.js](https://github.com/vuejs/core) - 渐进式 JavaScript 框架（MIT）
- [xterm.js](https://github.com/xtermjs/xterm.js) - 前端终端组件（MIT）

## 📄 许可证

[MIT](LICENSE)

## ⚠️ 免责声明

本项目仅供技术研究与学习使用。请确保您拥有所控制设备的合法使用权，并遵守当地法律法规。
