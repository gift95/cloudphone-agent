#!/bin/bash

if [ "$1" == "-h" ] || [ "$1" == "--help" ]; then
    echo "Usage: ./run.sh [adb-serial] [options]"
    echo "Example: ./run.sh -id my-device -signaling ws://192.168.50.100:8443"
    echo "         ./run.sh -id my-device -signaling ws://192.168.50.100:8443 -ice-servers \"turn:user:pass@192.168.50.100:3478?transport=udp,stun:192.168.50.100:3478\""
    echo "         ./run.sh -id my-redroid -signaling ws://192.168.50.100:8443 -external-addr 192.168.50.101 -webrtc-port 50000"
    echo ""
    echo "Options:"
    echo "  -signaling     Signaling server WebSocket URL (required)"
    echo "  -id            Device ID (required)"
    echo "  -jar           Path to scrcpy-server jar/so (default: /data/local/tmp/libsys_core.so)"
    echo "  -bitrate       Target video bitrate in bps"
    echo "  -max-bitrate   Max video bitrate in bps"
    echo "  -max-size      Max video size (0=unlimited)"
    echo "  -max-fps       Max video fps (0=unlimited)"
    echo "  -resolution    Video resolution WxH"
    echo "  -audio         Enable audio (true/false, default: true)"
    echo "  -debug         Enable debug logging"
    echo "  -timezone      Log timezone (e.g., Asia/Shanghai, UTC)"
    echo "  -ice-servers   Comma separated ICE servers (e.g., turn:user:pass@host:port?transport=udp,stun:host:port)"
    echo "  -external-addr External IP for NAT1To1IP"
    echo "  -webrtc-port   WebRTC UDP port (default: 50000)"
    exit 0
fi

SERIAL=""
# 如果第一个参数不是以 '-' 开头，则认为它是 adb serial
if [ $# -gt 0 ] && [[ ! "$1" == -* ]]; then
    SERIAL="-s $1"
    shift
fi

# 收集所有剩余参数作为 agent 的命令行参数
AGENT_ARGS="$@"

# 探测目标设备架构
ARCH=$(adb $SERIAL shell uname -m | tr -d '\r')
if [[ "$ARCH" == *"x86_64"* ]] || [[ "$ARCH" == *"amd64"* ]] || [[ "$ARCH" == *"i686"* ]] || [[ "$ARCH" == *"x86"* ]] || [[ "$ARCH" == *"i386"* ]]; then
    AGENT_BIN="cloudphone-agent-amd64"
elif [[ "$ARCH" == *"aarch64"* ]] || [[ "$ARCH" == *"arm64"* ]]; then
    AGENT_BIN="cloudphone-agent-arm64"
else
    AGENT_BIN="cloudphone-agent-armeabi-v7a"
fi

echo "=== Deploying to device ($ARCH) ==="
echo "Pushing $AGENT_BIN..."
adb $SERIAL push "$AGENT_BIN" /data/local/tmp/cloudphone-agent-arm64
echo "Pushing libsys_core.so..."
adb $SERIAL push libsys_core.so /data/local/tmp/libsys_core.so
adb $SERIAL shell chmod +x /data/local/tmp/cloudphone-agent-arm64

echo "=== Starting Agent ==="
# 1. 强制清理旧进程 (Agent & scrcpy-server)
adb $SERIAL shell "pkill -f cloudphone-agent || true"
adb $SERIAL shell "pkill -f com.genymobile.scrcpy || true"

# 2. 后台静默启动并保存日志
START_CMD="cd /data/local/tmp && CLASSPATH=/data/local/tmp/libsys_core.so nohup /data/local/tmp/cloudphone-agent-arm64 $AGENT_ARGS > /data/local/tmp/self-agent.log 2>&1 &"
echo "Executing: $START_CMD"
adb $SERIAL shell "sh -c '$START_CMD'"

# 3. 验证启动结果
sleep 2
if adb $SERIAL shell "ps -A | grep cloudphone-agent" > /dev/null; then
    echo "=== [OK] Agent started successfully in background. ==="
    echo "Log file: /data/local/tmp/self-agent.log"
else
    echo "=== [ERROR] Agent failed to start. Please check logs on device. ==="
    adb $SERIAL shell "tail -30 /data/local/tmp/self-agent.log"
fi
