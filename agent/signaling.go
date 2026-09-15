package main

import (
	"bytes"
	"crypto/tls"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
)

// customDNSServers 固定使用的公共 DNS 服务器（按优先级顺序）
var customDNSServers = []string{
	"8.8.8.8:53",
	"223.5.5.5:53",
	"1.1.1.1:53",
	"114.114.114.114:53",
	"119.29.29.29:53",
}

// isDNSError 判断错误是否为 DNS 解析错误
func isDNSError(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	errStr := err.Error()
	return strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "lookup ") ||
		strings.Contains(errStr, "Temporary failure in name resolution") ||
		(strings.Contains(errStr, "connection refused") && strings.Contains(errStr, ":53"))
}

// fallbackLookupHost 使用备用 DNS 服务器解析域名
func fallbackLookupHost(ctx context.Context, host string) ([]string, error) {
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			var lastErr error
			for _, server := range customDNSServers {
				conn, err := d.DialContext(ctx, "udp", server)
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, fmt.Errorf("all custom DNS servers unreachable: %v", lastErr)
		},
	}
	return resolver.LookupHost(ctx, host)
}

// customDialContext 固定使用自定义 DNS 解析域名后直连 IP（不使用系统 DNS）
func customDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}

	host, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return nil, fmt.Errorf("split host port: %v", splitErr)
	}

	// 如果已经是 IP 地址，直接连接
	if net.ParseIP(host) != nil {
		return d.DialContext(ctx, network, addr)
	}

	// 固定使用自定义 DNS 解析域名
	ips, lookupErr := fallbackLookupHost(ctx, host)
	if lookupErr != nil {
		return nil, fmt.Errorf("custom DNS lookup failed for %s: %v", host, lookupErr)
	}

	logf("[Signaling] custom DNS resolved %s -> %v", host, ips)

	var lastErr error
	for _, ip := range ips {
		ipAddr := net.JoinHostPort(ip, port)
		conn, connErr := d.DialContext(ctx, network, ipAddr)
		if connErr == nil {
			logf("[Signaling] connected via custom DNS to %s (%s)", host, ip)
			return conn, nil
		}
		lastErr = connErr
	}
	return nil, fmt.Errorf("all IPs unreachable after custom DNS resolution: %v", lastErr)
}

// signalingDialer 使用固定自定义 DNS 的 WebSocket dialer
var signalingDialer = websocket.Dialer{
	NetDialContext:   customDialContext,
	HandshakeTimeout: 15 * time.Second,
	// 跳过 TLS 证书验证（支持自签名证书的私有部署）
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
}

// SignalingClient WebSocket 信令（阶段 2 实测协议）：
//   注册 {device_id, device_info, is_webrtc:true, type:"agent_register"} → agent_register_ok{status:"valid"}
//   心跳 {type:"heartbeat"} 5s（无回执）
//   metrics {device_id, metrics{...}, type:"device_metrics"} 1s（无回执）
//   forward：服务器 → Agent 转发客户端消息（request-offer/answer/ice-candidate）
//            Agent → 服务器 forward（offer/ice-candidate），带 client_id 回显
type SignalingClient struct {
	cfg  *AgentConfig
	hub  *WebRTCHub
	conn *websocket.Conn

	writeMu  sync.Mutex // gorilla/websocket 不允许并发写
	mu       sync.Mutex
	clientID int // 服务器分配（forward 里回显）
	connected bool
	deviceInfo map[string]interface{}
}

func NewSignalingClient(cfg *AgentConfig, hub *WebRTCHub) *SignalingClient {
	return &SignalingClient{cfg: cfg, hub: hub}
}

func (s *SignalingClient) collectDeviceInfo() map[string]interface{} {
	w, h := s.cfg.resolutionWH()
	info := map[string]interface{}{
		"model":           getprop("ro.product.model", "unknown"),
		"brand":           getprop("ro.product.brand", "unknown"),
		"manufacturer":    getprop("ro.product.manufacturer", "unknown"),
		"device":          getprop("ro.product.device", "unknown"),
		"android_model":   getprop("ro.product.model", "unknown"),
		"android_serial":  getprop("ro.serialno", s.cfg.DeviceID),
		"android_version": getprop("ro.build.version.release", "unknown"),
		"android_sdk":     getprop("ro.build.version.sdk", "unknown"),
		"app_version":     Version,
		"is_webrtc":       true,
		"displays": []map[string]interface{}{
			{"x_res": w, "y_res": h, "width": w, "height": h},
		},
	}
	return info
}

// ConnectAndRegister 连接并注册，注册成功（agent_register_ok status=valid）后返回
func (s *SignalingClient) ConnectAndRegister() error {
	s.deviceInfo = s.collectDeviceInfo()

	conn, _, err := signalingDialer.Dial(s.cfg.Signaling, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", s.cfg.Signaling, err)
	}
	s.conn = conn

	reg := map[string]interface{}{
		"device_id":   s.cfg.DeviceID,
		"device_info": s.deviceInfo,
		"is_webrtc":   true,
		"type":        "agent_register",
	}
	msg, _ := json.Marshal(reg)
	logf("[Signaling] register: %s", string(msg))
	s.writeMu.Lock()
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		s.writeMu.Unlock()
		return err
	}
	s.writeMu.Unlock()

	// 等待 agent_register_ok
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read register ack: %w", err)
		}
		var ack map[string]interface{}
		if err := json.Unmarshal(data, &ack); err != nil {
			continue
		}
		if ack["message_type"] == "agent_register_ok" && ack["status"] == "valid" {
			logf("[Signaling] registered OK")
			s.connected = true
			go s.readLoop()
			return nil
		}
		logf("[Signaling] waiting ack, got: %s", string(data))
	}
	return fmt.Errorf("register ack timeout")
}

func (s *SignalingClient) readLoop() {
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			logf("[Signaling] read: %v", err)
			s.connected = false
			return
		}
		s.handleMessage(data)
	}
}

func (s *SignalingClient) handleMessage(data []byte) {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	mt, _ := m["message_type"].(string)
	payload, _ := m["payload"].(map[string]interface{})

	switch mt {
	case "forward":
		// 服务器转发客户端消息；client_id 用于回显
		if cid, ok := m["client_id"].(float64); ok {
			s.mu.Lock()
			s.clientID = int(cid)
			s.mu.Unlock()
		}
		if p, ok := m["payload"].(map[string]interface{}); ok {
			s.handleForward(p)
		} else if payload != nil {
			s.handleForward(payload)
		}
	case "client_disconnected":
		if cid, ok := m["client_id"].(float64); ok {
			s.hub.closeClient(int(cid))
			logf("[Signaling] client %d disconnected", int(cid))
		}
	case "group_control_event":
		// 前端群控指令：{"message_type":"group_control_event","event":{"type":"inject_keycode",...}}
		if ev, ok := m["event"].(map[string]interface{}); ok {
			s.handleGroupControlEvent(ev)
		}
	case "inject_data":
		// 数据注入：{"message_type":"inject_data","channel":"input","payload":{...}}
		ch, _ := m["channel"].(string)
		if payload, ok := m["payload"].(map[string]interface{}); ok {
			s.handleInjectData(ch, payload)
		}
	case "command":
		// 前端 shell 命令：{"message_type":"command","command":"input keyevent 26","request_id":"xxx"}
		cmd, _ := m["command"].(string)
		reqID, _ := m["request_id"].(string)
		if cmd != "" {
			go s.executeShellCommand(cmd, reqID)
		}
	case "start_preview", "stop_preview":
		// 前端预览控制：{"message_type":"start_preview","device_id":"...","fps":...,"bitrate":...,"stay_awake":...}
		s.handlePreviewControl(mt, m)
	}
}

// handlePreviewControl 处理预览控制消息（start_preview/stop_preview）
func (s *SignalingClient) handlePreviewControl(action string, m map[string]interface{}) {
	logf("[Signaling] preview control: %s", action)

	if action == "start_preview" {
		// 启用预览投屏
		s.hub.SetPreviewEnabled(true)

		// 立即请求关键帧，确保前端能快速解码第一帧
		if s.hub.sc != nil {
			if err := s.hub.sc.ResetVideo(); err != nil {
				logf("[Signaling] failed to request keyframe: %v", err)
			} else {
				logf("[Signaling] requested keyframe for preview")
			}
		}

		// 应用 stay_awake 设置
		if stayAwake, ok := m["stay_awake"].(bool); ok {
			s.applyStayAwake(stayAwake)
		}

		// 应用 bitrate 设置
		if bitrate, ok := m["bitrate"].(float64); ok && bitrate > 0 {
			s.hub.sc.SetBitrate(int(bitrate))
			logf("[Signaling] set bitrate: %d bps", int(bitrate))
		}

		// 应用 fps 设置（通过 scrcpy control 消息）
		if fps, ok := m["fps"].(float64); ok && fps > 0 {
			logf("[Signaling] set fps: %d", int(fps))
			// scrcpy 不支持动态修改 fps，这里仅记录
		}
	} else if action == "stop_preview" {
		// 停止预览投屏
		s.hub.SetPreviewEnabled(false)
	}
}

// applyStayAwake 应用保持设备唤醒设置
func (s *SignalingClient) applyStayAwake(enable bool) {
	if enable {
		// 禁止屏幕休眠：设置 screen_off_timeout 为最大值
		exec.Command("settings", "put", "system", "screen_off_timeout", "2147483647").Run()
		// 唤醒屏幕
		exec.Command("input", "keyevent", "224").Run()
		logf("[Signaling] stay awake enabled")
	} else {
		// 恢复默认屏幕超时（30秒）
		exec.Command("settings", "put", "system", "screen_off_timeout", "30000").Run()
		logf("[Signaling] stay awake disabled")
	}
}

// applyScrcpyOptions 应用前端传递的 scrcpy_options 设置
func (s *SignalingClient) applyScrcpyOptions(opts map[string]interface{}) {
	logf("[Signaling] applying scrcpy_options: %v", opts)

	// 摄像头监控模式：video_source/camera_facing/camera_id/camera_size 变化时需要重启 scrcpy-server
	needRestartForCamera := false
	if v, ok := opts["video_source"].(string); ok && v != "" {
		if v != s.cfg.VideoSource {
			logf("[Signaling] video_source changed: %s -> %s, will restart scrcpy-server", s.cfg.VideoSource, v)
			s.cfg.VideoSource = v
			needRestartForCamera = true
		}
	}
	// 解析摄像头参数，检测变化
	// camera_id 变化：重启 scrcpy-server（动态切换需要前端配合SPS/PPS，暂用重启方案）
	if v, ok := opts["camera_id"].(string); ok && v != s.cfg.CameraId && v != "" {
		logf("[Signaling] camera_id changed: %s -> %s, will restart scrcpy-server", s.cfg.CameraId, v)
		s.cfg.CameraId = v
		if s.cfg.VideoSource == "camera" {
			needRestartForCamera = true
		}
	}
	if v, ok := opts["camera_size"].(string); ok && v != s.cfg.CameraSize {
		logf("[Signaling] camera_size changed: %s -> %s", s.cfg.CameraSize, v)
		s.cfg.CameraSize = v
		if s.cfg.VideoSource == "camera" {
			needRestartForCamera = true
		}
	}
	// camera_facing 变化：重启 scrcpy-server（动态切换需要前端配合SPS/PPS，暂用重启方案）
	if v, ok := opts["camera_facing"].(string); ok && v != s.cfg.CameraFacing && v != "" {
		logf("[Signaling] camera_facing changed: %s -> %s, will restart scrcpy-server", s.cfg.CameraFacing, v)
		s.cfg.CameraFacing = v
		if s.cfg.VideoSource == "camera" {
			needRestartForCamera = true
		}
	}
	if v, ok := opts["camera_fps"].(float64); ok && v > 0 && int(v) != s.cfg.CameraFps {
		logf("[Signaling] camera_fps changed: %d -> %d", s.cfg.CameraFps, int(v))
		s.cfg.CameraFps = int(v)
		if s.cfg.VideoSource == "camera" {
			needRestartForCamera = true
		}
	}
	if v, ok := opts["camera_high_speed"].(bool); ok && v != s.cfg.CameraHighSpeed {
		s.cfg.CameraHighSpeed = v
		if s.cfg.VideoSource == "camera" {
			needRestartForCamera = true
		}
	}
	if v, ok := opts["camera_ar"].(string); ok && v != s.cfg.CameraAr {
		s.cfg.CameraAr = v
		if s.cfg.VideoSource == "camera" {
			needRestartForCamera = true
		}
	}
	// 如果 video_source 变化，重启 scrcpy-server（在 goroutine 中执行，避免阻塞信令处理）
	if needRestartForCamera {
		logf("[Signaling] restarting scrcpy-server for video_source=%s ...", s.cfg.VideoSource)
		go func() {
			if err := s.hub.sc.Restart(); err != nil {
				logf("[Signaling] scrcpy-server restart failed: %v", err)
			} else {
				logf("[Signaling] scrcpy-server restarted with video_source=%s", s.cfg.VideoSource)
				// 重启后重新设置 videoReader 回调函数
				time.Sleep(1 * time.Second)
				s.hub.RefreshVideoReader()
				logf("[Signaling] video reader callback refreshed after camera mode switch")
				// 重启后重新启动所有客户端的音频循环
				s.hub.RestartAudioLoops()
				logf("[Signaling] audio loops restarted after camera mode switch")
				// 重启后强制发送 keyframe，确保客户端能立即解码（避免黑屏）
				time.Sleep(500 * time.Millisecond)
				s.hub.sc.ResetVideo()
				logf("[Signaling] reset_video sent after restart (force keyframe)")
			}
		}()
		return // 重启后其他参数设置无效，直接返回
	}

	// stay_awake 保持设备唤醒
	if v, ok := opts["stay_awake"].(bool); ok {
		s.applyStayAwake(v)
	}

	// debug 调试日志
	if v, ok := opts["debug"].(bool); ok {
		s.cfg.Debug = v
		logf("[Signaling] debug mode: %v", v)
	}

	// bitrate 视频码率
	if v, ok := opts["bitrate"].(float64); ok && v > 0 {
		s.hub.sc.SetBitrate(int(v))
		logf("[Signaling] set bitrate: %d bps", int(v))
	}

	// max_fps 最大帧率
	if v, ok := opts["max_fps"].(float64); ok && v > 0 {
		logf("[Signaling] max fps: %d (scrcpy does not support dynamic fps change)", int(v))
	}

	// camera 摄像头注入
	if v, ok := opts["camera"].(bool); ok {
		logf("[Signaling] camera injection: %v", v)
		if v {
			// 摄像头注入需要 scrcpy-server 支持 Camera2 API
			// 当前版本仅记录，实际实现需要修改 scrcpy-server.jar
			logf("[Signaling] camera injection requested but not fully implemented in scrcpy-server")
		}
	}

	// audio 音频开关
	if v, ok := opts["audio"].(bool); ok {
		logf("[Signaling] audio enabled: %v", v)
		if !v {
			// 用户关闭音频：关闭音频连接，触发 scrcpy-server 音频编码器停止，释放音频设备
			// 这样手机就能恢复正常声音输出
			s.hub.sc.StopAudio()
			logf("[Signaling] audio connection closed, audio device released")
		} else if !s.hub.sc.IsAudioRunning() {
			// 用户开启音频，但音频连接已关闭（之前被停止过）
			// 需要重启 scrcpy-server 才能恢复音频捕获
			logf("[Signaling] audio requested but connection was stopped, restarting scrcpy-server...")
			// 在 goroutine 中重启，避免阻塞信令处理
			go func() {
				if err := s.hub.sc.Restart(); err != nil {
					logf("[Signaling] scrcpy-server restart failed: %v", err)
				} else {
					logf("[Signaling] scrcpy-server restarted, audio should be available")
					// 重启后重新设置 videoReader 回调函数，确保视频数据能正确发送
					time.Sleep(1 * time.Second)
					s.hub.RefreshVideoReader()
					logf("[Signaling] video reader callback refreshed after restart")
					// 重启后重新启动所有客户端的音频循环，确保音频数据能正确发送
					s.hub.RestartAudioLoops()
					logf("[Signaling] audio loops restarted after restart")
					// 重启后强制发送 keyframe，确保客户端能立即解码（避免黑屏）
					time.Sleep(500 * time.Millisecond)
					s.hub.sc.ResetVideo()
					logf("[Signaling] reset_video sent after restart (force keyframe)")
				}
			}()
		}
	}

	// power_off 息屏连接唤醒（录屏后切断背光，防窥省电）
	if v, ok := opts["power_off"].(bool); ok && v {
		logf("[Signaling] power off screen (turn off display power)")
		// 通过 scrcpy control 消息设置屏幕电源模式为 off（0）
		// 这会关闭物理屏幕背光，但不影响投屏（SurfaceControl 仍然可以捕获屏幕内容）
		if err := s.hub.sc.WriteControl(EncodeSetDisplayPower(0)); err != nil {
			logf("[Signaling] set display power off error: %v", err)
			// 降级方案：通过 settings 将屏幕亮度设为 0
			exec.Command("settings", "put", "system", "screen_brightness_mode", "0").Run()
			exec.Command("settings", "put", "system", "screen_brightness", "0").Run()
			logf("[Signaling] fallback: screen brightness set to 0")
		} else {
			logf("[Signaling] display power set to off (backlight off)")
		}
	} else if v, ok := opts["power_off"].(bool); ok && !v {
		// 恢复屏幕电源
		logf("[Signaling] power on screen (restore display power)")
		if err := s.hub.sc.WriteControl(EncodeSetDisplayPower(2)); err != nil {
			logf("[Signaling] set display power normal error: %v", err)
			// 降级方案：恢复屏幕亮度
			exec.Command("settings", "put", "system", "screen_brightness", "100").Run()
			logf("[Signaling] fallback: screen brightness restored to 100")
		} else {
			logf("[Signaling] display power restored to normal")
		}
	}
}

func (s *SignalingClient) handleForward(p map[string]interface{}) {
	t, _ := p["type"].(string)
	switch t {
	case "request-offer":
		logf("[Signaling] request-offer from client (cid=%d)", s.clientID)
		// 提取 scrcpy_options 和 ip_preference
		scrcpyOpts, _ := p["scrcpy_options"].(map[string]interface{})
		ipPref, _ := p["ip_preference"].(string)
		if scrcpyOpts != nil {
			s.applyScrcpyOptions(scrcpyOpts)
		}
		if ipPref != "" {
			logf("[Signaling] ip preference: %s", ipPref)
		}
		s.hub.HandleRequestOffer(s.clientID)
	case "answer":
		sdp, _ := p["sdp"].(string)
		if sdp == "" {
			if o, ok := p["answer"].(map[string]interface{}); ok {
				sdp, _ = o["sdp"].(string)
			}
		}
		logf("[Signaling] answer received (%d bytes)", len(sdp))
		// 诊断：dump answer SDP 供 fmtp/编解码协商分析
		if len(sdp) > 0 {
			_ = os.WriteFile("/data/local/tmp/answer.sdp", []byte(sdp), 0644)
		}
		s.mu.Lock()
		cid := s.clientID
		s.mu.Unlock()
		s.hub.HandleAnswer(cid, sdp)
	case "ice-candidate":
		cand, _ := p["candidate"].(map[string]interface{})
		if cand == nil {
			return
		}
		init := &webrtc.ICECandidateInit{
			Candidate:        str(cand["candidate"]),
			SDPMid:           strPtr(cand["sdpMid"]),
			SDPMLineIndex:    intPtr(cand["sdpMLineIndex"]),
			UsernameFragment: strPtr(cand["usernameFragment"]),
		}
		s.mu.Lock()
		cid := s.clientID
		s.mu.Unlock()
		s.hub.HandleICECandidate(cid, init)
	}
}

// handleGroupControlEvent 处理前端群控指令（按键/文字/滚动等）
func (s *SignalingClient) handleGroupControlEvent(ev map[string]interface{}) {
	t, _ := ev["type"].(string)
	switch t {
	case "inject_keycode":
		action := byte(toInt(ev["action"]))
		keycode := int32(toInt(ev["keycode"]))
		repeat := int32(toInt(ev["repeat"]))
		meta := int32(toInt(ev["meta"]))
		if err := s.hub.sc.WriteControl(EncodeInjectKeycode(action, keycode, repeat, meta)); err != nil {
			logf("[Signaling] inject_keycode WriteControl error: %v", err)
		}
		logf("[Signaling] inject_keycode action=%d keycode=%d", action, keycode)
	case "inject_text":
		text, _ := ev["text"].(string)
		if err := s.hub.sc.WriteControl(EncodeInjectText(text)); err != nil {
			logf("[Signaling] inject_text WriteControl error: %v", err)
		}
		logf("[Signaling] inject_text %d bytes", len(text))
	case "inject_scroll":
		x := int32(toInt(ev["x"]))
		y := int32(toInt(ev["y"]))
		w := uint16(toInt(ev["screen_width"]))
		h := uint16(toInt(ev["screen_height"]))
		scrollH := float32(toFloat(ev["scroll_h"]))
		scrollV := float32(toFloat(ev["scroll_v"]))
		if err := s.hub.sc.WriteControl(EncodeInjectScrollEvent(x, y, w, h, scrollH, scrollV, 0)); err != nil {
			logf("[Signaling] inject_scroll WriteControl error: %v", err)
		}
		logf("[Signaling] inject_scroll x=%d y=%d h=%.1f v=%.1f", x, y, scrollH, scrollV)
	case "set_clipboard":
		text, _ := ev["text"].(string)
		seq := uint64(toInt(ev["sequence"]))
		paste := toBool(ev["paste"])
		if err := s.hub.sc.WriteControl(EncodeSetClipboard(seq, text, paste)); err != nil {
			logf("[Signaling] set_clipboard WriteControl error: %v", err)
		}
		logf("[Signaling] set_clipboard %d bytes paste=%v", len(text), paste)
	case "back_or_screen_on":
		action := byte(toInt(ev["action"]))
		if err := s.hub.sc.WriteControl(EncodeBackOrScreenOn(action)); err != nil {
			logf("[Signaling] back_or_screen_on WriteControl error: %v", err)
		}
		logf("[Signaling] back_or_screen_on action=%d", action)
	case "touch":
		// 前端触控事件：{"type":"touch","action":0,"x":...,"y":...,"w":...,"h":...,"id":...}
		// action: 0=DOWN, 1=UP, 2=MOVE（与 Android MotionEvent 标准一致）
		action := byte(toInt(ev["action"]))
		pointerID := uint64(toInt(ev["id"]))
		// scrcpy PointersState 要求 pointerId >= 0；前端传 -1 表示主指针，转为 0
		if pointerID == 0xFFFFFFFFFFFFFFFF {
			pointerID = 0
		}
		x := float32(toFloat(ev["x"]))
		y := float32(toFloat(ev["y"]))
		w := uint16(toInt(ev["w"]))
		h := uint16(toInt(ev["h"]))
		if w == 0 || h == 0 {
			w, h = 1080, 2400
		}
		// 触控坐标映射：scrcpy PositionMapper 要求 screenW/H 与视频编码尺寸一致，
		// 前端传的是设备原始分辨率（1080x2400），需缩放到视频编码尺寸（如 368x800）
		if vw, vh := s.hub.sc.GetVideoSize(); vw > 0 && vh > 0 {
			x = x * float32(vw) / float32(w)
			y = y * float32(vh) / float32(h)
			w, h = uint16(vw), uint16(vh)
		}
		// 压力：DOWN/MOVE 保持 1.0（手指按下），UP=0（松开）
		pressure := float32(1.0)
		if action == 1 { // scrcpy UP
			pressure = 0
		}
		xInt := int32(x)
		yInt := int32(y)
		if err := s.hub.sc.WriteControl(EncodeInjectTouchEvent(action, pointerID, xInt, yInt, w, h, pressure, 0, 0)); err != nil {
			logf("[Signaling] touch WriteControl error: %v", err)
		}
		logf("[Signaling] touch action=%d id=%d x=%d y=%d w=%d h=%d pressure=%.2f", action, pointerID, xInt, yInt, w, h, pressure)
	case "open_app":
		// 批量打开应用：通过 monkey 命令启动指定包名的应用
		go s.handleOpenApp(ev)
	case "file_pull":
		// 批量文件拉取任务：从信令服务器下载文件并保存/安装
		go s.handleFilePull(ev)
	default:
		logf("[Signaling] unknown group_control_event type: %s", t)
	}
}

// handleOpenApp 处理批量打开应用任务
func (s *SignalingClient) handleOpenApp(ev map[string]interface{}) {
	taskID, _ := ev["task_id"].(string)
	packageName, _ := ev["package_name"].(string)

	// 从 payload 中提取包名（如果有的话）
	if packageName == "" {
		if payload, ok := ev["payload"].(map[string]interface{}); ok {
			packageName, _ = payload["package_name"].(string)
		}
	}
	// 兼容：payload 可能直接是包名字符串
	if packageName == "" {
		if p, ok := ev["payload"].(string); ok {
			packageName = p
		}
	}

	logf("[OpenApp] task=%s package=%s", taskID, packageName)

	if packageName == "" {
		s.sendTaskProgress(taskID, "failed", 0, "缺少包名")
		return
	}

	// 上报任务开始
	s.sendTaskProgress(taskID, "running", 10, "正在启动应用: "+packageName)

	// 使用 monkey 命令启动应用（自动找到 LAUNCHER Activity）
	cmd := exec.Command("sh", "-c", fmt.Sprintf("monkey -p %s -c android.intent.category.LAUNCHER 1", packageName))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	if err != nil {
		logf("[OpenApp] monkey error: %v, stderr: %s", err, stderr.String())
		s.sendTaskProgress(taskID, "failed", 0, "启动失败: "+err.Error())
		return
	}

	logf("[OpenApp] monkey output: %s", stdout.String())
	s.sendTaskProgress(taskID, "success", 100, "应用已启动: "+packageName)
}

// handleFilePull 处理文件拉取任务（从信令服务器下载文件并保存/安装）
func (s *SignalingClient) handleFilePull(ev map[string]interface{}) {
	taskID, _ := ev["task_id"].(string)
	fileName, _ := ev["file_name"].(string)
	fileURL, _ := ev["file_url"].(string)
	action, _ := ev["action"].(string) // "install" 或 "push"
	targetPath, _ := ev["target_path"].(string)

	// 从 payload 中提取字段（如果有的话）
	if payload, ok := ev["payload"].(map[string]interface{}); ok {
		if fileName == "" {
			fileName, _ = payload["file_name"].(string)
		}
		if fileURL == "" {
			fileURL, _ = payload["file_url"].(string)
		}
		if action == "" {
			action, _ = payload["action"].(string)
		}
		if targetPath == "" {
			targetPath, _ = payload["target_path"].(string)
		}
	}

	logf("[FilePull] task=%s file=%s url=%s action=%s", taskID, fileName, fileURL, action)

	// 上报任务开始
	s.sendTaskProgress(taskID, "downloading", 0, "开始下载文件")

	// 如果没有提供 URL，构造默认 URL（信令服务器 /files/{filename}）
	if fileURL == "" && fileName != "" {
		// 从信令服务器地址构造 URL
		fileURL = fmt.Sprintf("http://%s/files/%s", s.getSignalingHost(), fileName)
	}

	if fileURL == "" {
		s.sendTaskProgress(taskID, "failed", 0, "缺少文件 URL")
		return
	}

	// 下载文件
	savePath := filepath.Join("/data/local/tmp", fileName)
	if targetPath != "" {
		savePath = targetPath
	}

	downloaded, err := s.downloadFile(fileURL, savePath, taskID)
	if err != nil {
		logf("[FilePull] download error: %v", err)
		s.sendTaskProgress(taskID, "failed", 0, "下载失败: "+err.Error())
		return
	}

	logf("[FilePull] downloaded: %s (%d bytes)", savePath, downloaded)

	// 根据 action 处理文件
	if action == "install" || (action == "" && isAPKFile(fileName)) {
		// 安装 APK：重命名为英文文件名（避免中文路径问题），安装后删除
		s.sendTaskProgress(taskID, "installing", 90, "开始安装 APK")

		// 创建临时英文文件名
		tempAPKPath := "/data/local/tmp/install_temp.apk"

		// 如果原文件不是临时路径，复制到临时路径
		if savePath != tempAPKPath {
			if err := copyFile(savePath, tempAPKPath); err != nil {
				logf("[FilePull] copy to temp error: %v", err)
				s.sendTaskProgress(taskID, "failed", 0, "复制文件失败: "+err.Error())
				return
			}
			logf("[FilePull] copied to temp: %s", tempAPKPath)
		}

		// 安装 APK
		installErr := s.installAPK(tempAPKPath)

		// 安装完成后删除临时文件和原始下载文件
		os.Remove(tempAPKPath)
		logf("[FilePull] removed temp file: %s", tempAPKPath)
		if savePath != tempAPKPath {
			os.Remove(savePath)
			logf("[FilePull] removed original file: %s", savePath)
		}

		if installErr != nil {
			logf("[FilePull] install error: %v", installErr)
			s.sendTaskProgress(taskID, "failed", 0, "安装失败: "+installErr.Error())
			return
		}
		s.sendTaskProgress(taskID, "success", 100, "安装成功")
	} else {
		// 仅保存文件
		s.sendTaskProgress(taskID, "success", 100, fmt.Sprintf("文件已保存: %s (%d bytes)", savePath, downloaded))
	}
}

// downloadFile 从 URL 下载文件到指定路径
func (s *SignalingClient) downloadFile(url string, destPath string, taskID string) (int64, error) {
	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// 确保目标目录存在
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return 0, err
	}

	out, err := os.Create(destPath)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	// 带进度的复制
	var downloaded int64
	total := resp.ContentLength
	buf := make([]byte, 32*1024)
	lastProgress := -1

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return downloaded, werr
			}
			downloaded += int64(n)

			// 上报进度（每 10% 上报一次）
			if total > 0 {
				progress := int(downloaded * 100 / total)
				if progress != lastProgress && progress%10 == 0 {
					lastProgress = progress
					s.sendTaskProgress(taskID, "downloading", progress, fmt.Sprintf("下载中 %d%%", progress))
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return downloaded, err
		}
	}

	return downloaded, nil
}

// installAPK 安装 APK 文件
func (s *SignalingClient) installAPK(apkPath string) error {
	cmd := exec.Command("pm", "install", "-r", apkPath)
	// 不等待安装结果，只确认命令成功拉起
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动安装命令失败: %v", err)
	}

	// 等待 2 秒，检查命令是否在启动阶段就失败
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		// 命令在 2 秒内退出
		if err != nil {
			// 尝试获取错误输出
			if output, ok := err.(*exec.ExitError); ok {
				return fmt.Errorf("安装失败: %s", string(output.Stderr))
			}
			return fmt.Errorf("安装失败: %v", err)
		}
		// 命令在 2 秒内成功完成
		logf("[FilePull] install completed quickly")
	case <-time.After(2 * time.Second):
		// 命令还在运行，说明安装命令已成功拉起
		logf("[FilePull] install command started successfully, not waiting for result")
	}

	return nil
}

// copyFile 复制文件
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}

	return destFile.Sync()
}

// sendTaskProgress 上报任务进度
func (s *SignalingClient) sendTaskProgress(taskID string, status string, progress int, message string) {
	msg := map[string]interface{}{
		"message_type": "task_progress",
		"device_id":    s.cfg.DeviceID,
		"task_id":      taskID,
		"status":       status,
		"progress":     progress,
		"message":      message,
	}
	s.SendMessage(msg)
	logf("[FilePull] task=%s status=%s progress=%d%% message=%s", taskID, status, progress, message)
}

// getSignalingHost 从信令服务器 URL 中提取 host:port
func (s *SignalingClient) getSignalingHost() string {
	// 从配置的 signaling 地址中提取 host:port
	// 例如 ws://192.168.50.100:8443/register_agent -> 192.168.50.100:8443
	signaling := s.cfg.Signaling
	if signaling == "" {
		return "192.168.50.100:8443"
	}
	// 简单解析
	if len(signaling) > 5 && signaling[:5] == "ws://" {
		signaling = signaling[5:]
	}
	if len(signaling) > 6 && signaling[:6] == "wss://" {
		signaling = signaling[6:]
	}
	// 去掉路径部分
	if idx := indexOf(signaling, '/'); idx >= 0 {
		signaling = signaling[:idx]
	}
	return signaling
}

// indexOf 返回字符串中第一个匹配字符的索引
func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// isAPKFile 判断是否为 APK 文件
func isAPKFile(filename string) bool {
	return len(filename) > 4 && filename[len(filename)-4:] == ".apk"
}

// handleInjectData 处理数据注入消息
func (s *SignalingClient) handleInjectData(ch string, payload map[string]interface{}) {
	logf("[Signaling] inject_data channel=%s payload=%v", ch, payload)
	// 目前与 group_control_event 共用处理逻辑
	if t, ok := payload["type"].(string); ok {
		payload["type"] = t
		s.handleGroupControlEvent(payload)
	}
}

// executeShellCommand 在设备上执行 shell 命令并回传结果
func (s *SignalingClient) executeShellCommand(cmd string, reqID string) {
	logf("[Signaling] exec command: %s", cmd)
	var stdoutBuf, stderrBuf bytes.Buffer
	c := exec.Command("sh", "-c", cmd)
	c.Stdout = &stdoutBuf
	c.Stderr = &stderrBuf
	err := c.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
			stderrBuf.WriteString(err.Error())
		}
	}
	stdout := stdoutBuf.String()
	stderr := stderrBuf.String()
	logf("[Signaling] command result: exit=%d stdout=%d bytes stderr=%d bytes", exitCode, len(stdout), len(stderr))
	// 回传命令执行结果（前端期望格式：stdout/stderr/exit_code）
	resp := map[string]interface{}{
		"message_type": "command_result",
		"device_id":    s.cfg.DeviceID,
		"request_id":   reqID,
		"stdout":       stdout,
		"stderr":       stderr,
		"exit_code":    exitCode,
	}
	s.SendMessage(resp)
}

// SendForward Agent → 服务器（带 client_id 回显）
func (s *SignalingClient) SendForward(payload map[string]interface{}) {	s.mu.Lock()
	cid := s.clientID
	s.mu.Unlock()
	msg := map[string]interface{}{
		"message_type": "forward",
		"device_id":    s.cfg.DeviceID,
		"payload":      payload,
	}
	if cid > 0 {
		msg["client_id"] = cid
	}
	data, _ := json.Marshal(msg)
	s.writeMu.Lock()
	err := s.conn.WriteMessage(websocket.TextMessage, data)
	s.writeMu.Unlock()
	if err != nil {
		logf("[Signaling] send forward: %v", err)
	}
}

func (s *SignalingClient) SendMessage(m map[string]interface{}) {
	data, _ := json.Marshal(m)
	s.writeMu.Lock()
	err := s.conn.WriteMessage(websocket.TextMessage, data)
	s.writeMu.Unlock()
	if err != nil {
		logf("[Signaling] send: %v", err)
	}
}

// SendPreviewBinary 发送二进制预览视频帧给信令服务器
func (s *SignalingClient) SendPreviewBinary(data []byte) {
	if !s.connected || s.conn == nil {
		return
	}
	s.writeMu.Lock()
	err := s.conn.WriteMessage(websocket.BinaryMessage, data)
	s.writeMu.Unlock()
	if err != nil {
		logf("[Signaling] send preview binary: %v", err)
	}
}

// HeartbeatLoop 5s 心跳
func (s *SignalingClient) HeartbeatLoop() {
	t := time.NewTicker(5 * time.Second)
	for range t.C {
		if !s.connected {
			return
		}
		s.SendMessage(map[string]interface{}{"type": "heartbeat"})
	}
}

// MetricsLoop 1s device_metrics（无 message_type，阶段 2 协议铁律）
func (s *SignalingClient) MetricsLoop() {
	t := time.NewTicker(1 * time.Second)
	for range t.C {
		if !s.connected {
			return
		}
		metrics := collectMetrics()
		msg := map[string]interface{}{
			"device_id": s.cfg.DeviceID,
			"metrics":   metrics,
			"type":      "device_metrics",
		}
		s.SendMessage(msg)
	}
}

// 工具
func str(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toBool(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t == "true" || t == "1"
	}
	return false
}

func strPtr(v interface{}) *string {
	if s, ok := v.(string); ok && s != "" {
		return &s
	}
	return nil
}

func intPtr(v interface{}) *uint16 {
	switch t := v.(type) {
	case float64:
		u := uint16(t)
		return &u
	case int:
		u := uint16(t)
		return &u
	}
	return nil
}
