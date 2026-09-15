#!/system/bin/sh

# 清理可能正在运行的 Agent 进程及其 scrcpy 进程
pkill -f "cloudphone-agent-arm64" || true
pkill -f "com.genymobile.scrcpy" || true

# 清理在系统上生成的临时日志
rm -f /data/local/tmp/self-agent.log
