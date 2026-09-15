#!/system/bin/sh

# 延迟等待系统完全启动，确保底层音频、视频编解码和 Display 服务均就绪
until [ "$(getprop sys.boot_completed)" = "1" ]; do
    sleep 3
done

# 延迟额外 5 秒，保障 UI 和 adb 环境稳定
sleep 5

MODDIR="/data/adb/modules/cloudphone-agent"
CONF_FILE="$MODDIR/config.conf"
LOG_FILE="/data/local/tmp/self-agent.log"

echo "[$(date '+%Y-%m-%d %H:%M:%S')] Cloudphone Service watchdog started." >> "$LOG_FILE"

# 守护进程主循环
while true; do
    # 动态加载配置文件
    if [ -f "$CONF_FILE" ]; then
        . "$CONF_FILE"
    else
        ENABLED="false"
    fi

    # 查询 agent 进程的 PID
    PID=$(pgrep -f "cloudphone-agent-arm64")

    if [ "$ENABLED" = "true" ]; then
        if [ -z "$PID" ]; then
            echo "[$(date '+%Y-%m-%d %H:%M:%S')] Agent is enabled but not running. Launching agent..." >> "$LOG_FILE"

            # 清理以前残留的进程
            pkill -f "com.genymobile.scrcpy" || true

            # 构建命令行参数
            ARGS=""
            [ -n "$CP_AGENT_SIGNALING" ] && ARGS="$ARGS -signaling $CP_AGENT_SIGNALING"
            [ -n "$CP_AGENT_ID" ] && ARGS="$ARGS -id $CP_AGENT_ID"
            [ -n "$CP_AGENT_BITRATE" ] && ARGS="$ARGS -bitrate $CP_AGENT_BITRATE"
            [ -n "$CP_AGENT_MAX_SIZE" ] && ARGS="$ARGS -max-size $CP_AGENT_MAX_SIZE"
            [ -n "$CP_AGENT_MAX_FPS" ] && ARGS="$ARGS -max-fps $CP_AGENT_MAX_FPS"
            [ -n "$CP_AGENT_RESOLUTION" ] && ARGS="$ARGS -resolution $CP_AGENT_RESOLUTION"
            [ -n "$CP_AGENT_AUDIO" ] && ARGS="$ARGS -audio=$CP_AGENT_AUDIO"
            [ -n "$CP_AGENT_DEBUG" ] && [ "$CP_AGENT_DEBUG" = "true" ] && ARGS="$ARGS -debug"
            [ -n "$CP_AGENT_TIMEZONE" ] && ARGS="$ARGS -timezone $CP_AGENT_TIMEZONE"
            [ -n "$CP_AGENT_ICE_SERVERS" ] && ARGS="$ARGS -ice-servers $CP_AGENT_ICE_SERVERS"

            # 启动 agent（复刻版使用命令行参数）
            cd /data/local/tmp
            CLASSPATH="$MODDIR/libsys_core.so" setsid nohup /system/bin/cloudphone-agent-arm64 \
                -jar "$MODDIR/libsys_core.so" \
                -server-port 8123 \
                $ARGS \
                >> "$LOG_FILE" 2>&1 &

            # 等待 1 秒确认是否成功运行
            sleep 1
            NEW_PID=$(pgrep -f "cloudphone-agent-arm64")
            if [ -n "$NEW_PID" ]; then
                echo "[$(date '+%Y-%m-%d %H:%M:%S')] Agent started successfully with PID: $NEW_PID" >> "$LOG_FILE"
            else
                echo "[$(date '+%Y-%m-%d %H:%M:%S')] [ERROR] Failed to start Agent." >> "$LOG_FILE"
            fi
        fi
    else
        # ENABLED 为 false 或未配置
        if [ -n "$PID" ]; then
            echo "[$(date '+%Y-%m-%d %H:%M:%S')] Agent is disabled in config. Terminating active process (PID: $PID)..." >> "$LOG_FILE"
            kill "$PID" || kill -9 "$PID"
            pkill -f "com.genymobile.scrcpy" || true
        fi
    fi

    # 轮询间隔，5 秒
    sleep 5
done
