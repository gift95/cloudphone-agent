package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

// CloudPhone Agent（自研复刻版）
// 架构：Android 上拉起移植版 scrcpy-server（com.genymobile.scrcpy.CoreService，
// TCP 四通道 v/a/t/c），解析 H.264 视频流经 WebRTC 推给浏览器，
// DataChannel 控制消息转译成 scrcpy 控制协议字节流下发。
func main() {
	cfg := parseConfig()

	// 设置日志时区
	if cfg.Timezone != "" {
		SetLogTimezone(cfg.Timezone)
	}

	// 1. 确保 jar 存在（如果不存在，从嵌入的资源中释放）
	if err := ensureScrcpyServerJar(cfg.JarPath); err != nil {
		logf("ERROR: failed to ensure libsys_core.so: %s (%v)", cfg.JarPath, err)
		os.Exit(1)
	}

	// 2. 启动 scrcpy-server（先监听端口再拉起 CoreService）
	sc := NewScrcpyServer(cfg)
	if err := sc.Start(); err != nil {
		logf("ERROR: start scrcpy-server: %v", err)
		os.Exit(1)
	}

	// 3. WebRTC Hub（等待 request-offer 触发）
	hub := NewWebRTCHub(cfg, sc)

	// 4. 信令注册
	sig := NewSignalingClient(cfg, hub)
	hub.SetForwardFn(sig.SendForward)
	hub.SetSendPreviewFrame(sig.SendPreviewBinary)

	for i := 0; i < 5; i++ {
		err := sig.ConnectAndRegister()
		if err == nil {
			break
		}
		logf("[Signaling] connect attempt %d failed: %v", i+1, err)
		time.Sleep(3 * time.Second)
		if i == 4 {
			logf("ERROR: signaling registration failed after retries")
			os.Exit(1)
		}
	}

	// 5. 心跳 + metrics
	go sig.HeartbeatLoop()
	go sig.MetricsLoop()

	// 6. 视频扇出
	go hub.StartVideoFanout()

	logf("CloudPhone Agent (self-built) ready: id=%s signaling=%s", cfg.DeviceID, cfg.Signaling)

	// 优雅退出
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	sc.Stop()
}
