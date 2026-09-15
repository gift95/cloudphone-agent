# 云手机 Agent 安卓应用（复刻版）

将 `cloudphone-agent`（WebRTC 远程流控 Agent）封装为安卓应用，实现：

- **开机自启**：`BOOT_COMPLETED` 广播自动拉起 Agent（可在应用内关闭）
- **Shizuku 免 root 启动**：通过 [Shizuku](https://github.com/RikkaApps/Shizuku) 以 shell 权限运行 `/data/local/tmp/cloudphone-agent-arm64`，无需 root
- **前端配置页**：所有参数可在应用内配置，等价于命令行参数

## 默认参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `-signaling` | `ws://192.168.50.100:8443/register_agent` | 信令服务器地址 |
| `-id` | 自动生成 `device-android-<model>` | 设备 ID |
| `-server-port` | `8133` | Scrcpy 服务端口 |
| `-bitrate` | `2000000` | 视频码率 (bps) |
| `-audio` | `true` | 启用音频 |
| `-debug` | `false` | 调试日志 |

## 构建

环境要求：JDK 17+、Android SDK（platform 34 / build-tools 34）。

```bash
# 首次构建（自动下载 Gradle 与依赖）
./gradlew assembleDebug
```

产物：
- Debug：`app/build/outputs/apk/debug/app-debug.apk`（自动使用 debug 签名）

## 安装与使用

1. **安装并激活 Shizuku**（二选一）：
   - ADB 激活（电脑）：`adb shell sh /sdcard/Android/data/moe.shizuku.privileged.api/start.sh`
   - 无线调试激活：Shizuku 应用内引导
2. **安装 APK**：`adb install -r app-debug.apk`
3. **打开应用** → 点击「申请 Shizuku 权限」→ 授权
4. 配置信令服务器地址和设备 ID
5. 点击「启动 Agent」，或保持「开机自启」开启，重启后自动运行
6. 日志：`/data/local/tmp/self-agent.log`，应用内「运行日志」区域实时查看

## 运行原理

应用将 assets 中的 `cloudphone-agent-arm64` 与 `libsys_core.so`
释放到外部存储专属目录，按配置生成启动脚本（使用命令行参数），
通过 Shizuku 以 shell 身份：

```sh
cp <私有目录>/cloudphone-agent-arm64 /data/local/tmp/
cp <私有目录>/libsys_core.so /data/local/tmp/
cd /data/local/tmp
CLASSPATH=/data/local/tmp/libsys_core.so setsid nohup /data/local/tmp/cloudphone-agent-arm64 \
  -signaling ws://192.168.50.100:8443/register_agent \
  -id device-xxx \
  -jar /data/local/tmp/libsys_core.so \
  -server-port 8133 \
  -bitrate 2000000 \
  -audio=true \
  >/data/local/tmp/self-agent.log 2>&1 &
```

Agent 以 `setsid` 独立进程运行，与应用生命周期无关。

## 目录结构

```
android-app/
├── app/
│   ├── build.gradle
│   └── src/
│       └── main/
│           ├── AndroidManifest.xml
│           ├── assets/
│           │   ├── cloudphone-agent-arm64    # Go Agent 二进制
│           │   └── libsys_core.so             # scrcpy-server (APK 改扩展名)
│           ├── java/com/cloudphone/agent/
│           │   ├── MainActivity.java           # 配置界面
│           │   ├── AgentBootstrap.java         # Agent 启动引导
│           │   ├── AgentConfig.java            # 配置管理
│           │   ├── AgentService.java           # 前台服务
│           │   ├── BootReceiver.java           # 开机自启
│           │   └── ShizukuLauncher.java       # Shizuku 权限封装
│           └── res/
├── build.gradle
├── settings.gradle
└── README.md
```
