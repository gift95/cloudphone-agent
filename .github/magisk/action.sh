#!/system/bin/sh

MODDIR="/data/adb/modules/cloudphone-agent"
CONF_FILE="$MODDIR/config.conf"
LOG_FILE="/data/local/tmp/self-agent.log"
CTL_BIN="/system/bin/cloudphone-ctl"

echo "=============================================="
echo "      Cloudphone Agent 快捷管理控制台"
echo "=============================================="

if [ ! -f "$CONF_FILE" ] || [ ! -x "$CTL_BIN" ]; then
    echo "[ERROR] 模块未正确安装，缺少控制脚本或配置！"
    exit 1
fi

# 加载当前配置
. "$CONF_FILE"

PID=$(pgrep -f "cloudphone-agent-arm64")

echo " 当前保活开关 (Watchdog): $ENABLED"
if [ -n "$PID" ]; then
    echo " Agent 进程状态:         RUNNING (PID: $PID)"
    echo "----------------------------------------------"
    echo " [动作] 检测到服务运行中，正在为您关闭服务..."
    $CTL_BIN stop
else
    echo " Agent 进程状态:         STOPPED"
    echo "----------------------------------------------"
    echo " [动作] 检测到服务未运行，正在为您开启服务..."
    $CTL_BIN start
fi

echo "----------------------------------------------"
# 重新加载最新配置和进程状态
. "$CONF_FILE"
NEW_PID=$(pgrep -f "cloudphone-agent-arm64")
echo " 新的保活开关 (Watchdog): $ENABLED"
if [ -n "$NEW_PID" ]; then
    echo " 新的 Agent 进程:        RUNNING (PID: $NEW_PID)"
    echo ""
    echo " 信令服务地址: $CP_AGENT_SIGNALING"
    echo " 设备唯一 ID:  ${CP_AGENT_ID:-(自动生成)}"
    echo " ICE 服务器:   ${CP_AGENT_ICE_SERVERS:-(默认)}"
else
    echo " 新的 Agent 进程:        STOPPED"
fi

echo "=============================================="
echo " 近期运行日志 (最后 10 行):"
echo "----------------------------------------------"
tail -n 10 "$LOG_FILE" 2>/dev/null || echo "(无日志记录)"
echo "=============================================="
