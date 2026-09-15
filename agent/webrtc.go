package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/pion/webrtc/v3"
	"golang.org/x/sys/unix"
	"github.com/pion/webrtc/v3/pkg/media"
)

// WebRTCHub 管理所有客户端 PeerConnection（Fat Agent 直连模式：每 client 一 PC）
type WebRTCHub struct {
	cfg *AgentConfig
	sc  *ScrcpyServer

	mu      sync.Mutex
	clients map[int]*WebRTCClient
	nextID  int

	// 音频发送控制（不关闭音频连接，只控制是否发送数据，避免重启 scrcpy-server）
	audioEnabled bool

	// 预览投屏控制（WebSocket H.264 裸流）
	previewEnabled bool
	previewFrameCount int
	previewFrameSkip int  // 预览帧率控制：每 N 帧发送一帧（关键帧除外）

	// 信令回调（发 forward 消息给服务器）
	sendForward func(payload map[string]interface{})
	// 发送预览视频帧给信令服务器（二进制）
	sendPreviewFrame func(data []byte)
}

// SetAudioEnabled 设置是否发送音频数据（不关闭音频连接）
func (h *WebRTCHub) SetAudioEnabled(enabled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.audioEnabled != enabled {
		h.audioEnabled = enabled
		logf("[WebRTC] audio sending %s", map[bool]string{true: "enabled", false: "disabled"}[enabled])
	}
}

// IsAudioEnabled 检查是否发送音频数据
func (h *WebRTCHub) IsAudioEnabled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.audioEnabled
}

type WebRTCClient struct {
	id   int
	pc   *webrtc.PeerConnection
	hub  *WebRTCHub

	videoTrack *webrtc.TrackLocalStaticSample
	audioTrack *webrtc.TrackLocalStaticRTP
	inputDC    *webrtc.DataChannel
	clipboardDC *webrtc.DataChannel
	cameraDC   *webrtc.DataChannel
	fileDC     *webrtc.DataChannel
	adbDC      *webrtc.DataChannel

	// ADB 交互式终端
	adbShell   *exec.Cmd
	adbStdin   io.WriteCloser
	adbStdout  io.ReadCloser
	adbStderr  io.ReadCloser
	adbRunning bool

	// 文件上传状态
	uploadFile     *os.File
	uploadPath     string
	uploadSize     int64
	uploadReceived int64
	uploadSHA256   string
	uploadInstall  bool

	lastKeyframe []byte // 最近关键帧（含 SPS/PPS），用于 PLI 重发
	lastKeyPTS   uint64
	frameCount   int64

	// 基于编码器 PTS 的稳定时间戳和动态 Duration
	baseTime    time.Time
	basePTS     uint64
	lastPTS     uint64
	lastDuration time.Duration
}

func NewWebRTCHub(cfg *AgentConfig, sc *ScrcpyServer) *WebRTCHub {
	return &WebRTCHub{
		cfg:          cfg,
		sc:           sc,
		clients:      map[int]*WebRTCClient{},
		audioEnabled: cfg.Audio, // 默认根据配置启用音频
	}
}

func (h *WebRTCHub) SetForwardFn(fn func(payload map[string]interface{})) {
	h.sendForward = fn
}

// SetPreviewEnabled 设置是否启用预览投屏（WebSocket H.264 裸流）
func (h *WebRTCHub) SetPreviewEnabled(enabled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.previewEnabled = enabled
	logf("[Preview] enabled=%v", enabled)
}

// IsPreviewEnabled 检查是否启用了预览投屏
func (h *WebRTCHub) IsPreviewEnabled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.previewEnabled
}

// SetSendPreviewFrame 设置发送预览视频帧的回调
func (h *WebRTCHub) SetSendPreviewFrame(fn func(data []byte)) {
	h.sendPreviewFrame = fn
}

// sendPreviewSample 构建并发送预览视频帧（二进制格式）
// 格式：PREV(4) + device_id(32) + is_key(1) + pts_us(8) + payload_len(4) + nalu_data
func (h *WebRTCHub) sendPreviewSample(sample VideoSample) {
	if h.sendPreviewFrame == nil {
		logf("[Preview] ERROR: sendPreviewFrame callback is nil")
		return
	}
	deviceID := h.cfg.DeviceID
	// 构建二进制帧
	headerSize := 4 + 32 + 1 + 8 + 4
	buf := make([]byte, headerSize+len(sample.Data))
	// Magic: PREV
	buf[0] = 0x50
	buf[1] = 0x52
	buf[2] = 0x45
	buf[3] = 0x56
	// DeviceID (32 bytes, null-padded)
	idBytes := []byte(deviceID)
	if len(idBytes) > 31 {
		idBytes = idBytes[:31]
	}
	copy(buf[4:36], idBytes)
	// is_key
	if sample.KeyFrame {
		buf[36] = 0x01
	} else {
		buf[36] = 0x00
	}
	// pts_us (uint64, big-endian)
	binary.BigEndian.PutUint64(buf[37:45], uint64(sample.PTS))
	// payload_len (uint32, big-endian)
	binary.BigEndian.PutUint32(buf[45:49], uint32(len(sample.Data)))
	// nalu_data
	copy(buf[49:], sample.Data)
	h.sendPreviewFrame(buf)

	// 调试日志：每 30 帧记录一次
	h.previewFrameCount++
	if h.previewFrameCount%30 == 1 {
		logf("[Preview] sent frame #%d size=%d key=%v pts=%d", h.previewFrameCount, len(sample.Data), sample.KeyFrame, sample.PTS)
	}
}

// StartVideoFanout 将视频样本广播给所有活跃客户端
func (h *WebRTCHub) StartVideoFanout() {
	// videoReader 在 CoreService 连回 video 端口后才创建，轮询等待其就绪
	for i := 0; i < 60; i++ {
		if h.sc.videoReader != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if h.sc.videoReader == nil {
		logf("[Video] fanout: video reader not ready within 30s")
		return
	}
	// 启动 BWE 带宽估计动态码率调整
	go h.bweLoop()
	h.RefreshVideoReader()
}

// RefreshVideoReader 重新设置 videoReader 的回调函数
// 在 scrcpy-server 重启后调用，确保新的 videoReader 能正确发送视频数据
func (h *WebRTCHub) RefreshVideoReader() {
	if h.sc.videoReader == nil {
		logf("[Video] RefreshVideoReader: videoReader is nil")
		return
	}
	logf("[Video] RefreshVideoReader: setting sample callback for new videoReader")
	h.sc.videoReader.SetSampleCallback(h.videoSampleCallback)
	// scrcpy-server 重启后，PTS 会重新从 0 开始
	// 重置所有客户端的 PTS 状态，避免 duration 计算错误（跳变）
	h.mu.Lock()
	for _, c := range h.clients {
		c.lastPTS = 0
		c.basePTS = 0
		c.baseTime = time.Time{}
		c.lastDuration = 0
		logf("[Video] client %d PTS state reset after scrcpy restart", c.id)
	}
	h.mu.Unlock()
}

// RestartAudioLoops 重新启动所有客户端的音频循环
// 在 scrcpy-server 重启后调用，确保新的音频连接能正确发送音频数据
func (h *WebRTCHub) RestartAudioLoops() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.clients {
		if c.pc == nil || c.audioTrack == nil {
			continue
		}
		logf("[Audio] RestartAudioLoops: restarting audio loop for client %d", c.id)
		go c.audioLoop()
	}
}

// videoSampleCallback 视频样本回调函数，将视频数据发送给所有客户端
func (h *WebRTCHub) videoSampleCallback(sample VideoSample) {
	// 预览投屏：如果启用了预览，将视频帧通过 WebSocket 发送给信令服务器
	// 注意：在锁外发送，避免阻塞视频扇出
	if h.IsPreviewEnabled() {
		h.sendPreviewSample(sample)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.clients {
		if c.pc == nil || c.videoTrack == nil {
			continue
		}
		if sample.KeyFrame {
			c.lastKeyframe = sample.Data
			c.lastKeyPTS = sample.PTS
		}
		c.frameCount++
		if c.frameCount%30 == 1 {
			logf("[Video] client %d write sample #%d size=%d key=%v pts=%d state=%s", c.id, c.frameCount, len(sample.Data), sample.KeyFrame, sample.PTS, c.pc.ConnectionState())
		}

		// 硬件级 PTS 时间戳直通（HW-PTS Passthrough）
		// 使用编码器输出的微秒级物理渲染时间戳作为 RTP 时间戳基准
		// 第一个帧建立基准：baseTime = 当前时间，basePTS = 编码器 PTS
		// 后续帧：Timestamp = baseTime + (sample.PTS - basePTS) 微秒
		if c.basePTS == 0 {
			c.basePTS = sample.PTS
			c.baseTime = time.Now()
			logf("[Video] client %d PTS baseline established: basePTS=%d baseTime=%v", c.id, c.basePTS, c.baseTime)
		}

		// 动态 Duration（基于实际帧间隔，使用编码器 PTS）
		var duration time.Duration
		if c.lastPTS > 0 && sample.PTS >= c.lastPTS {
			duration = time.Duration(sample.PTS-c.lastPTS) * time.Microsecond
			// 限制 Duration 在合理范围（1fps 到 120fps）
			if duration < time.Second/120 {
				duration = time.Second / 120
			}
			if duration > time.Second {
				duration = time.Second
			}
			c.lastDuration = duration
		} else {
			duration = c.lastDuration
			if duration == 0 {
				duration = time.Second / 30
			}
		}
		c.lastPTS = sample.PTS

		// 基于编码器 PTS 计算 Timestamp（硬件级 PTS 直通）
		// 而不是使用 time.Now()，确保 RTP 时间戳与编码器 PTS 一致
		var timestamp time.Time
		if sample.PTS >= c.basePTS {
			timestamp = c.baseTime.Add(time.Duration(sample.PTS-c.basePTS) * time.Microsecond)
		} else {
			// PTS 跳变（小于基准），重新建立基准
			c.basePTS = sample.PTS
			c.baseTime = time.Now()
			timestamp = c.baseTime
			logf("[Video] client %d PTS jump detected, re-established baseline: basePTS=%d", c.id, c.basePTS)
		}

		if err := c.videoTrack.WriteSample(media.Sample{
			Data:      sample.Data,
			Duration:  duration,
			Timestamp: timestamp,
		}); err != nil {
			logf("[Video] client %d WriteSample error: %v", c.id, err)
		}
	}
}

// HandleRequestOffer 收到前端 request-offer 后创建 PeerConnection 并发 offer
// cid 为服务器分配的 client_id（forward 路由标识，必须与服务器一致）
func (h *WebRTCHub) HandleRequestOffer(cid int) {
	if cid <= 0 {
		// 服务器未附 client_id 时退化为本地自增（不影响单客户端场景）
		h.mu.Lock()
		cid = h.nextID
		h.nextID++
		h.mu.Unlock()
	}

	c := &WebRTCClient{id: cid, hub: h}
	if err := c.create(h.cfg); err != nil {
		logf("[WebRTC] create peer %d: %v", cid, err)
		return
	}
	offer, err := c.pc.CreateOffer(nil)
	if err != nil {
		logf("[WebRTC] create offer: %v", err)
		return
	}
	if err := c.pc.SetLocalDescription(offer); err != nil {
		logf("[WebRTC] set local desc: %v", err)
		return
	}
	logf("[WebRTC] client %d offer created, sending", cid)
	h.sendForward(map[string]interface{}{
		"type": "offer",
		"sdp":  offer.SDP,
	})

	h.mu.Lock()
	h.clients[cid] = c
	h.mu.Unlock()

	// 连接建立后立即请求一次关键帧，并启动帧保活（编码器静止时强制刷新）
	go h.videoKeepalive(cid)
}

// videoKeepalive 视频保活（与原版一致：不重复关键帧、不 reset，仅连接建立时请求关键帧+唤醒屏幕）
func (h *WebRTCHub) videoKeepalive(cid int) {
	_ = h.sc.ResetVideo() // 连接建立立即请求关键帧（control 未连时静默失败）
	// 唤醒屏幕：锁屏/熄屏时 SurfaceControl 无合成、编码器停流
	if err := exec.Command("input", "keyevent", "224").Run(); err != nil {
		logf("[Video] wake screen: %v", err)
	}
	// 仅监控客户端存活，不做帧重复或 reset（与原版一致，避免打乱帧时序导致 JB 增大）
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		h.mu.Lock()
		_, alive := h.clients[cid]
		h.mu.Unlock()
		if !alive {
			return
		}
	}
}

// HandleAnswer 前端 answer
func (h *WebRTCHub) HandleAnswer(id int, sdp string) {
	h.mu.Lock()
	c := h.clients[id]
	h.mu.Unlock()
	if c == nil {
		logf("[WebRTC] answer for unknown client %d", id)
		return
	}
	if err := c.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}); err != nil {
		logf("[WebRTC] set remote desc: %v", err)
	}
}

// HandleICECandidate 前端 ICE candidate
func (h *WebRTCHub) HandleICECandidate(id int, cand *webrtc.ICECandidateInit) {
	h.mu.Lock()
	c := h.clients[id]
	h.mu.Unlock()
	if c == nil {
		return
	}
	if err := c.pc.AddICECandidate(*cand); err != nil {
		logf("[WebRTC] add ice candidate: %v", err)
	}
}

func (h *WebRTCHub) closeClient(id int) {
	h.mu.Lock()
	c := h.clients[id]
	delete(h.clients, id)
	h.mu.Unlock()
	if c != nil {
		_ = c.pc.Close()
	}
}

// parseICEServer 解析 ICE 服务器 URL，支持 turn:user:pass@host:port 格式
// 标准 TURN URL (RFC 7065) 不包含 userinfo，用户名密码需通过 ICEServer.Username/Credential 单独设置
func parseICEServer(raw string) webrtc.ICEServer {
	server := webrtc.ICEServer{URLs: []string{raw}}

	// 只处理 turn/turns 协议
	if !strings.HasPrefix(raw, "turn:") && !strings.HasPrefix(raw, "turns:") {
		return server
	}

	// 提取 scheme 和 rest
	var scheme, rest string
	if strings.HasPrefix(raw, "turns:") {
		scheme = "turns:"
		rest = raw[6:]
	} else {
		scheme = "turn:"
		rest = raw[5:]
	}

	// 检查是否包含 @ (userinfo)
	atIdx := strings.LastIndex(rest, "@")
	if atIdx < 0 {
		return server
	}

	userInfo := rest[:atIdx]
	hostPart := rest[atIdx+1:]

	// 解析 user:pass
	colonIdx := strings.Index(userInfo, ":")
	if colonIdx < 0 {
		return server
	}
	username := userInfo[:colonIdx]
	password := userInfo[colonIdx+1:]

	// 清理后的 URL（去掉 userinfo）
	cleanURL := scheme + hostPart
	logf("[WebRTC] TURN server parsed: url=%s username=%s", cleanURL, username)

	return webrtc.ICEServer{
		URLs:       []string{cleanURL},
		Username:   username,
		Credential: password,
	}
}

func (c *WebRTCClient) create(cfg *AgentConfig) error {
	config := webrtc.Configuration{}
	iceServers := cfg.iceServerList()
	for _, s := range iceServers {
		config.ICEServers = append(config.ICEServers, parseICEServer(s))
	}
	if len(config.ICEServers) == 0 {
		config.ICEServers = []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}
	}

	settingEngine := webrtc.SettingEngine{}
	if cfg.ExternalAddr != "" {
		// NAT 外部地址（局域网部署时如 Agent 位于 NAT 后）
		settingEngine.SetNAT1To1IPs([]string{cfg.ExternalAddr}, webrtc.ICECandidateTypeHost)
	}
	// MediaEngine 必须显式注册 H264（Pion 默认不含）
	// profile-level-id=42e01f：Baseline profile Level 3.1，与原版一致
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return fmt.Errorf("register default codecs: %w", err)
	}
	h264Fmtp := "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"
	videoCodecParams := cfg.VideoCodec.RTPCodecParams()
	if cfg.VideoCodec == VideoCodecH264 {
		videoCodecParams.SDPFmtpLine = h264Fmtp
	}
	if err := mediaEngine.RegisterCodec(videoCodecParams, webrtc.RTPCodecTypeVideo); err != nil {
		return fmt.Errorf("register %s codec: %w", cfg.VideoCodec, err)
	}
	logf("[WebRTC] video codec: %s (mime=%s, pt=%d)", cfg.VideoCodec, videoCodecParams.MimeType, videoCodecParams.PayloadType)
	// 音频编解码器（根据 cfg.AudioCodec 动态选择，默认 Opus 48kHz 立体声）
	audioCodecParams := cfg.AudioCodec.RTPCodecParams()
	if err := mediaEngine.RegisterCodec(audioCodecParams, webrtc.RTPCodecTypeAudio); err != nil {
		return fmt.Errorf("register %s codec: %w", cfg.AudioCodec, err)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithSettingEngine(settingEngine))

	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("NewPeerConnection: %w", err)
	}
	c.pc = pc

	// 视频轨道（根据 cfg.VideoCodec 动态选择编码格式）
	videoTrackFactory := NewVideoTrackFactory(cfg.VideoCodec)
	videoTrack, err := videoTrackFactory.CreateTrack()
	if err != nil {
		pc.Close()
		return fmt.Errorf("create video track: %w", err)
	}
	c.videoTrack = videoTrack
	if _, err := pc.AddTrack(videoTrack); err != nil {
		pc.Close()
		return fmt.Errorf("add track: %w", err)
	}

	// Opus 音频轨道（scrcpy 音频格式：48kHz 立体声 Opus）
	if cfg.Audio {
		audioTrack, err := webrtc.NewTrackLocalStaticRTP(
			webrtc.RTPCodecCapability{
				MimeType:    "audio/opus",
				ClockRate:   48000,
				Channels:    2,
				SDPFmtpLine: "minptime=10;useinbandfec=1",
			},
			"audio", fmt.Sprintf("audio-%d", c.id))
		if err != nil {
			pc.Close()
			return fmt.Errorf("create audio track: %w", err)
		}
		c.audioTrack = audioTrack
		if _, err := pc.AddTrack(audioTrack); err != nil {
			pc.Close()
			return fmt.Errorf("add audio track: %w", err)
		}
	}

	// DataChannels（Agent 侧创建）
	if dc, err := pc.CreateDataChannel("input-channel", nil); err == nil {
		c.inputDC = dc
		dc.OnOpen(func() { logf("[WebRTC] client %d input-channel OPEN", c.id) })
		dc.OnMessage(c.onInputMessage)
	}
	if dc, err := pc.CreateDataChannel("clipboard-channel", nil); err == nil {
		c.clipboardDC = dc
		dc.OnOpen(func() { logf("[WebRTC] client %d clipboard-channel OPEN", c.id) })
		dc.OnMessage(c.onClipboardMessage)
	}
	if dc, err := pc.CreateDataChannel("camera-channel", nil); err == nil {
		c.cameraDC = dc
		dc.OnOpen(func() { logf("[WebRTC] client %d camera-channel OPEN", c.id) })
		dc.OnMessage(c.onCameraMessage)
	}

	// 前端主动创建的通道（file/adb/ai-command）
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		logf("[WebRTC] client %d ondatachannel: %s", c.id, dc.Label())
		switch dc.Label() {
		case "file-channel":
			c.fileDC = dc
			dc.OnMessage(c.onFileMessage)
		case "adb-channel":
			c.adbDC = dc
			dc.OnMessage(c.onAdbMessage)
		case "ai-command-channel":
			dc.OnMessage(c.onAICommandMessage)
		}
	})

	pc.OnICECandidate(func(ice *webrtc.ICECandidate) {
		if ice == nil {
			return
		}
		c.hub.sendForward(map[string]interface{}{
			"type": "ice-candidate",
			"candidate": map[string]interface{}{
				"candidate":        ice.ToJSON().Candidate,
				"sdpMid":           ice.ToJSON().SDPMid,
				"sdpMLineIndex":    ice.ToJSON().SDPMLineIndex,
				"usernameFragment": ice.ToJSON().UsernameFragment,
			},
		})
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		logf("[WebRTC] client %d connection state: %s", c.id, state)
		if state == webrtc.PeerConnectionStateConnected {
			// 连接建立后立即请求关键帧，确保浏览器能收到第一个关键帧
			// （连接建立前发送的关键帧可能丢失，导致后续 P帧无法解码黑屏）
			go func() {
				time.Sleep(100 * time.Millisecond)
				_ = c.hub.sc.ResetVideo()
			}()
			// 启动音频循环
			if c.audioTrack != nil {
				go c.audioLoop()
			}
		}
		if state == webrtc.PeerConnectionStateDisconnected || state == webrtc.PeerConnectionStateFailed ||
			state == webrtc.PeerConnectionStateClosed {
			c.hub.closeClient(c.id)
		}
	})

	// 注：Pion v3.3.6 发送轨不做 PLI 拦截；scrcpy 关键帧间隔短（GOP 1~2s），
	// 前端 PLI 后自然关键帧即可恢复，无需显式处理。

	return nil
}

// ---- DataChannel 消息处理 ----

// onInputMessage 前端 input-channel：touch / inject_scroll / inject_text / inject_keycode
func (c *WebRTCClient) onInputMessage(msg webrtc.DataChannelMessage) {
	var m map[string]interface{}
	if err := json.Unmarshal(msg.Data, &m); err != nil {
		logf("[Input] bad json: %v", err)
		return
	}
	t, _ := m["type"].(string)
	switch t {
	case "touch":
		c.handleTouch(m)
	case "inject_scroll":
		c.handleScroll(m)
	case "inject_text":
		text, _ := m["text"].(string)
		if text != "" {
			_ = c.hub.sc.WriteControl(EncodeInjectText(text))
			logf("[Input] inject_text %d bytes", len(text))
		}
	case "inject_keycode":
		action := byte(toInt(m["action"]))
		keycode := int32(toInt(m["keycode"]))
		repeat := int32(toInt(m["repeat"]))
		meta := int32(toInt(m["meta"]))
		_ = c.hub.sc.WriteControl(EncodeInjectKeycode(action, keycode, repeat, meta))
		logf("[Input] inject_keycode action=%d keycode=%d", action, keycode)
	case "screenshot":
		c.handleScreenshot(m)
	}
}

// handleScreenshot 截图并通过 DataChannel 回传
func (c *WebRTCClient) handleScreenshot(m map[string]interface{}) {
	requestID, _ := m["request_id"].(string)
	logf("[Screenshot] request received, request_id=%s", requestID)

	// 使用 screencap 命令截图
	screenshotPath := "/data/local/tmp/screenshot.png"
	cmd := exec.Command("screencap", "-p", screenshotPath)
	if err := cmd.Run(); err != nil {
		logf("[Screenshot] screencap error: %v", err)
		c.sendScreenshotResult(requestID, "", err.Error())
		return
	}

	// 读取截图文件
	data, err := os.ReadFile(screenshotPath)
	if err != nil {
		logf("[Screenshot] read file error: %v", err)
		c.sendScreenshotResult(requestID, "", err.Error())
		return
	}

	// 转换为 base64
	encoded := base64.StdEncoding.EncodeToString(data)
	logf("[Screenshot] success, size=%d bytes, base64=%d bytes", len(data), len(encoded))
	c.sendScreenshotResult(requestID, encoded, "")
}

func (c *WebRTCClient) sendScreenshotResult(requestID, data, errMsg string) {
	resp := map[string]interface{}{
		"type":       "screenshot_result",
		"request_id": requestID,
	}
	if data != "" {
		resp["data"] = data
	}
	if errMsg != "" {
		resp["error"] = errMsg
	}
	respBytes, err := json.Marshal(resp)
	if err != nil {
		logf("[Screenshot] marshal error: %v", err)
		return
	}
	if c.inputDC != nil {
		c.inputDC.SendText(string(respBytes))
	}
}

// handleTouch 前端 touch JSON → scrcpy INJECT_TOUCH_EVENT
// 前端坐标已映射到设备逻辑分辨率（x/y/w/h），scrcpy 侧按 screenW/H 换算
// action 编码：前端与 scrcpy 协议一致，均为 0=DOWN 1=UP 2=MOVE（Android MotionEvent 标准）
func (c *WebRTCClient) handleTouch(m map[string]interface{}) {
	action := byte(toInt(m["action"]))
	pointerID := uint64(toInt(m["id"]))
	// scrcpy PointersState 要求 pointerId >= 0；前端传 -1 表示主指针，转为 0
	if pointerID == 0xFFFFFFFFFFFFFFFF {
		pointerID = 0
	}
	x := float32(toFloat(m["x"]))
	y := float32(toFloat(m["y"]))
	w := uint16(toInt(m["w"]))
	h := uint16(toInt(m["h"]))
	if w == 0 || h == 0 {
		w, h = 1080, 2400
	}
	// 触控坐标映射：scrcpy PositionMapper 要求 screenW/H 与视频编码尺寸一致，
	// 前端传的是设备原始分辨率（1080x2400），需缩放到视频编码尺寸（如 864x1920）
	if vw, vh := c.hub.sc.GetVideoSize(); vw > 0 && vh > 0 {
		x = x * float32(vw) / float32(w)
		y = y * float32(vh) / float32(h)
		w, h = uint16(vw), uint16(vh)
	}
	// 压力：DOWN/MOVE 保持 1.0（手指按下），UP=0（松开）
	// 不使用前端传来的 pressure（前端 MOVE 事件可能传 0，导致触控被误判为抬起）
	pressure := float32(1.0)
	if action == 1 { // scrcpy UP
		pressure = 0
	}
	// x/y 取整为 int32（scrcpy 3.3.4 协议用 int32 像素坐标，parsePosition 用 dis.readInt()）
	xInt := int32(x)
	yInt := int32(y)
	if err := c.hub.sc.WriteControl(EncodeInjectTouchEvent(action, pointerID, xInt, yInt, w, h, pressure, 0, 0)); err != nil {
		logf("[Input] WriteControl touch error: %v", err)
	}
	debugf(c.hub.cfg.Debug, "[Input] touch action=%d id=%d x=%d y=%d w=%d h=%d pressure=%.2f", action, pointerID, xInt, yInt, w, h, pressure)
}

func (c *WebRTCClient) handleScroll(m map[string]interface{}) {
	x := float32(toFloat(m["x"]))
	y := float32(toFloat(m["y"]))
	w := uint16(toInt(m["w"]))
	h := uint16(toInt(m["h"]))
	// 触控坐标映射到视频编码尺寸（同 handleTouch）
	if vw, vh := c.hub.sc.GetVideoSize(); vw > 0 && vh > 0 && w > 0 && h > 0 {
		x = x * float32(vw) / float32(w)
		y = y * float32(vh) / float32(h)
		w, h = uint16(vw), uint16(vh)
	}
	scrollH := float32(toFloat(m["scroll_h"]))
	scrollV := float32(toFloat(m["scroll_v"]))
	if err := c.hub.sc.WriteControl(EncodeInjectScrollEvent(int32(x), int32(y), w, h, scrollH, scrollV, 0)); err != nil {
		logf("[Input] WriteControl scroll error: %v", err)
	}
	logf("[Input] scroll x=%d y=%d h=%.1f v=%.1f", int32(x), int32(y), scrollH, scrollV)
}

// onClipboardMessage clipboard-channel：set_clipboard / get_clipboard
func (c *WebRTCClient) onClipboardMessage(msg webrtc.DataChannelMessage) {
	var m map[string]interface{}
	if err := json.Unmarshal(msg.Data, &m); err != nil {
		return
	}
	t, _ := m["type"].(string)
	switch t {
	case "set_clipboard":
		text, _ := m["text"].(string)
		paste := m["paste"] == true
		_ = c.hub.sc.WriteControl(EncodeSetClipboard(0, text, paste))
		logf("[Clipboard] set_clipboard paste=%v %d bytes", paste, len(text))
	case "get_clipboard":
		_ = c.hub.sc.WriteControl([]byte{TypeGetClipboard, 0})
	}
}

// onFileMessage file-channel：文件管理命令
func (c *WebRTCClient) onFileMessage(msg webrtc.DataChannelMessage) {
	// 二进制数据：文件上传分块
	if len(msg.Data) > 0 && msg.Data[0] != '{' {
		c.handleFileUploadChunk(msg.Data)
		return
	}

	if len(msg.Data) == 0 {
		return
	}

	var req map[string]interface{}
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		logf("[File] parse error: %v", err)
		return
	}

	reqType, _ := req["type"].(string)
	logf("[File] cmd: %s", reqType)

	switch reqType {
	case "list":
		c.handleFileList(req)
	case "mkdir":
		c.handleFileMkdir(req)
	case "delete":
		c.handleFileDelete(req)
	case "rename":
		c.handleFileRename(req)
	case "download_start":
		c.handleFileDownload(req)
	case "upload_start":
		c.handleFileUploadStart(req)
	case "install_apk":
		c.handleInstallAPK(req)
	default:
		logf("[File] unknown type: %s", reqType)
	}
}

func (c *WebRTCClient) sendFileResponse(resp map[string]interface{}) {
	data, err := json.Marshal(resp)
	if err != nil {
		logf("[File] marshal error: %v", err)
		return
	}
	// 输出响应前200字符用于调试
	preview := string(data)
	if len(preview) > 200 {
		preview = preview[:200] + "..."
	}
	logf("[File] send response: type=%v, size=%d bytes, fileDC=%v, readyState=%v, preview=%s",
		resp["type"], len(data), c.fileDC != nil,
		func() string {
			if c.fileDC != nil {
				return c.fileDC.ReadyState().String()
			}
			return "nil"
		}(), preview)
	if c.fileDC != nil {
		if err := c.fileDC.SendText(string(data)); err != nil {
			logf("[File] send error: %v", err)
		}
	}
}

// handleFileDownload 文件下载：读取文件并分块发送二进制数据
// 分块格式：[魔数"PREV":4][request_id:32][is_last:1][offset:8][size:4][data]
func (c *WebRTCClient) handleFileDownload(req map[string]interface{}) {
	path, _ := req["path"].(string)
	requestID, _ := req["request_id"].(string)
	logf("[File] download start: path=%s, request_id=%s", path, requestID)

	if path == "" {
		logf("[File] download error: empty path")
		return
	}

	// 读取文件
	data, err := os.ReadFile(path)
	if err != nil {
		logf("[File] download read error: %v", err)
		return
	}

	logf("[File] download file size: %d bytes", len(data))

	// 分块发送
	const chunkSize = 16 * 1024 // 16KB per chunk
	totalSize := len(data)
	offset := 0

	for offset < totalSize {
		end := offset + chunkSize
		if end > totalSize {
			end = totalSize
		}
		chunk := data[offset:end]
		isLast := 0
		if end >= totalSize {
			isLast = 1
		}

		// 构建分块头部：4(魔数) + 32(request_id) + 1(is_last) + 8(offset) + 4(size) = 49字节
		header := make([]byte, 49)
		// 魔数 "PREV"
		header[0] = 80 // 'P'
		header[1] = 82 // 'R'
		header[2] = 69 // 'E'
		header[3] = 86 // 'V'
		// request_id (32字节，以\0结尾)
		reqIDBytes := []byte(requestID)
		if len(reqIDBytes) > 31 {
			reqIDBytes = reqIDBytes[:31]
		}
		copy(header[4:4+len(reqIDBytes)], reqIDBytes)
		// is_last (1字节)
		header[36] = byte(isLast)
		// offset (8字节 BigEndian uint64)
		binary.BigEndian.PutUint64(header[37:45], uint64(offset))
		// size (4字节 BigEndian uint32)
		binary.BigEndian.PutUint32(header[45:49], uint32(len(chunk)))

		// 合并头部和数据
		packet := append(header, chunk...)

		// 发送二进制数据（使用 Send 而不是 SendText）
		if c.fileDC != nil {
			if err := c.fileDC.Send(packet); err != nil {
				logf("[File] download send chunk error at offset %d: %v", offset, err)
				return
			}
		}

		logf("[File] download sent chunk: offset=%d, size=%d, is_last=%d", offset, len(chunk), isLast)
		offset = end
	}

	logf("[File] download complete: %d bytes sent", totalSize)
}

// handleFileUploadStart 文件上传开始：创建文件并初始化上传状态
func (c *WebRTCClient) handleFileUploadStart(req map[string]interface{}) {
	path, _ := req["path"].(string)
	size := int64(toFloat(req["size"]))
	sha256, _ := req["sha256"].(string)
	installOnFinish, _ := req["install_on_finish"].(bool)

	logf("[File] upload start: path=%s, size=%d, sha256=%s, install=%v", path, size, sha256, installOnFinish)

	if path == "" {
		logf("[File] upload error: empty path")
		return
	}

	// 关闭之前未完成的上传
	if c.uploadFile != nil {
		c.uploadFile.Close()
		c.uploadFile = nil
	}

	// 创建文件
	f, err := os.Create(path)
	if err != nil {
		logf("[File] upload create error: %v", err)
		return
	}

	c.uploadFile = f
	c.uploadPath = path
	c.uploadSize = size
	c.uploadReceived = 0
	c.uploadSHA256 = sha256
	c.uploadInstall = installOnFinish

	logf("[File] upload file created: %s", path)
}

// handleFileUploadChunk 文件上传分块：写入文件数据
func (c *WebRTCClient) handleFileUploadChunk(data []byte) {
	if c.uploadFile == nil {
		logf("[File] upload chunk error: no active upload")
		return
	}

	n, err := c.uploadFile.Write(data)
	if err != nil {
		logf("[File] upload write error: %v", err)
		return
	}

	c.uploadReceived += int64(n)

	if c.uploadReceived% (1024*1024) == 0 || c.uploadReceived >= c.uploadSize {
		logf("[File] upload progress: %d/%d bytes (%.1f%%)",
			c.uploadReceived, c.uploadSize, float64(c.uploadReceived)/float64(c.uploadSize)*100)
	}

	// 上传完成
	if c.uploadReceived >= c.uploadSize {
		logf("[File] upload complete: %s (%d bytes)", c.uploadPath, c.uploadReceived)
		c.uploadFile.Close()
		c.uploadFile = nil

		// 可选：安装 APK
		if c.uploadInstall {
			go c.installAPK(c.uploadPath)
		}

		// 发送上传完成响应
		c.sendFileResponse(map[string]interface{}{
			"type":    "upload_complete",
			"path":    c.uploadPath,
			"size":    c.uploadReceived,
			"success": true,
		})
	}
}

// handleInstallAPK 安装 APK
func (c *WebRTCClient) handleInstallAPK(req map[string]interface{}) {
	path, _ := req["path"].(string)
	logf("[File] install apk: %s", path)
	go c.installAPK(path)
}

// installAPK 执行 pm install 命令安装 APK
func (c *WebRTCClient) installAPK(path string) {
	logf("[File] installing apk: %s", path)
	cmd := exec.Command("pm", "install", "-r", path)
	out, err := cmd.CombinedOutput()
	result := string(out)
	if err != nil {
		result += "\n[ERROR] " + err.Error()
		logf("[File] install apk error: %v", err)
	} else {
		logf("[File] install apk success: %s", result)
	}

	// 发送安装结果
	c.sendFileResponse(map[string]interface{}{
		"type":    "install_result",
		"path":    path,
		"result":  result,
		"success": err == nil,
	})
}

func (c *WebRTCClient) handleFileList(req map[string]interface{}) {
	path, _ := req["path"].(string)
	if path == "" {
		path = "/sdcard"
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		c.sendFileResponse(map[string]interface{}{
			"type":    "list_reply",
			"success": false,
			"path":    path,
			"error":   err.Error(),
		})
		return
	}

	files := make([]map[string]interface{}, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		filePath := path
		if !strings.HasSuffix(filePath, "/") {
			filePath += "/"
		}
		filePath += entry.Name()
		files = append(files, map[string]interface{}{
			"name":     entry.Name(),
			"path":     filePath,
			"is_dir":   entry.IsDir(),
			"size":     info.Size(),
			"mod_time": info.ModTime().Unix(),
		})
	}

	c.sendFileResponse(map[string]interface{}{
		"type":    "list_reply",
		"success": true,
		"path":    path,
		"files":   files,
	})
}

func (c *WebRTCClient) handleFileMkdir(req map[string]interface{}) {
	path, _ := req["path"].(string)
	if path == "" {
		c.sendFileResponse(map[string]interface{}{"type": "mkdir_reply", "success": false, "error": "path is empty"})
		return
	}
	err := os.MkdirAll(path, 0755)
	if err != nil {
		c.sendFileResponse(map[string]interface{}{"type": "mkdir_reply", "success": false, "path": path, "error": err.Error()})
		return
	}
	c.sendFileResponse(map[string]interface{}{"type": "mkdir_reply", "success": true, "path": path})
}

func (c *WebRTCClient) handleFileDelete(req map[string]interface{}) {
	path, _ := req["path"].(string)
	if path == "" {
		c.sendFileResponse(map[string]interface{}{"type": "delete_reply", "success": false, "error": "path is empty"})
		return
	}
	err := os.RemoveAll(path)
	if err != nil {
		c.sendFileResponse(map[string]interface{}{"type": "delete_reply", "success": false, "path": path, "error": err.Error()})
		return
	}
	c.sendFileResponse(map[string]interface{}{"type": "delete_reply", "success": true, "path": path})
}

func (c *WebRTCClient) handleFileRename(req map[string]interface{}) {
	oldPath, _ := req["old_path"].(string)
	newPath, _ := req["new_path"].(string)
	if oldPath == "" || newPath == "" {
		c.sendFileResponse(map[string]interface{}{"type": "rename_reply", "success": false, "error": "path is empty"})
		return
	}
	err := os.Rename(oldPath, newPath)
	if err != nil {
		c.sendFileResponse(map[string]interface{}{"type": "rename_reply", "success": false, "error": err.Error()})
		return
	}
	c.sendFileResponse(map[string]interface{}{"type": "rename_reply", "success": true})
}

// onAdbMessage adb-channel：交互式 Shell 终端
func (c *WebRTCClient) onAdbMessage(msg webrtc.DataChannelMessage) {
	if c.adbDC == nil {
		return
	}

	// 调试日志：打印收到的数据
	preview := string(msg.Data)
	if len(preview) > 64 {
		preview = preview[:64]
	}
	logf("[ADB] received %d bytes: %q (adbRunning=%v, adbStdin=%v)", len(msg.Data), preview, c.adbRunning, c.adbStdin != nil)

	// 尝试解析为 JSON 初始化消息
	var initMsg struct {
		Type string `json:"type"`
		Rows int    `json:"rows"`
		Cols int    `json:"cols"`
	}
	if err := json.Unmarshal(msg.Data, &initMsg); err == nil && initMsg.Type == "init" {
		logf("[ADB] init terminal: rows=%d cols=%d", initMsg.Rows, initMsg.Cols)
		c.startAdbShell(initMsg.Rows, initMsg.Cols)
		return
	}

	// 终端输入数据
	if c.adbRunning && c.adbStdin != nil {
		if _, err := c.adbStdin.Write(msg.Data); err != nil {
			logf("[ADB] write stdin error: %v", err)
		}
	}
}

// startAdbShell 启动交互式 shell（使用伪 tty）
func (c *WebRTCClient) startAdbShell(rows, cols int) {
	if c.adbRunning {
		return
	}

	logf("[ADB] starting interactive shell with PTY (rows=%d, cols=%d)", rows, cols)

	// 手动创建伪 tty：打开 /dev/ptmx
	masterFd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		logf("[ADB] open /dev/ptmx error: %v, fallback to sh -s", err)
		c.startAdbShellFallback()
		return
	}

	// 获取 slave 设备号
	// TIOCGPTN = 0x80045430 (获取从设备号)
	// TIOCSPTLCK = 0x40045431 (设置/清除锁, 0=解锁)
	slaveNum, err := unix.IoctlGetInt(masterFd, 0x80045430) // TIOCGPTN
	if err != nil {
		logf("[ADB] TIOCGPTN error: %v, fallback to sh -s", err)
		unix.Close(masterFd)
		c.startAdbShellFallback()
		return
	}
	logf("[ADB] PTY slave num=%d", slaveNum)

	// 解锁 slave (TIOCSPTLCK = 0x40045431, 需要传递指向 int 的指针)
	var unlock int = 0
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(masterFd), uintptr(0x40045431), uintptr(unsafe.Pointer(&unlock)))
	if errno != 0 {
		logf("[ADB] TIOCSPTLCK unlock error: %v (non-fatal)", errno)
	} else {
		logf("[ADB] TIOCSPTLCK unlock success")
	}

	// 打开 slave 设备
	slavePath := fmt.Sprintf("/dev/pts/%d", slaveNum)
	// 尝试 chmod（Android devpts 默认 mode=600）
	if err := unix.Chmod(slavePath, 0666); err != nil {
		logf("[ADB] chmod %s error: %v (non-fatal, trying open anyway)", slavePath, err)
	}

	// 检查文件是否存在以及权限
	var stat unix.Stat_t
	if err := unix.Stat(slavePath, &stat); err != nil {
		logf("[ADB] stat %s error: %v", slavePath, err)
	} else {
		logf("[ADB] slave %s exists: mode=%o uid=%d gid=%d", slavePath, stat.Mode, stat.Uid, stat.Gid)
	}

	// 尝试多种方式打开 slave
	var slaveFd int
	openErrors := []string{}
	for _, flags := range []int{unix.O_RDWR, unix.O_RDWR | unix.O_NOCTTY, unix.O_RDONLY | unix.O_WRONLY} {
		slaveFd, err = unix.Open(slavePath, flags, 0)
		if err == nil {
			logf("[ADB] opened slave %s with flags=0x%x", slavePath, flags)
			break
		}
		openErrors = append(openErrors, fmt.Sprintf("flags=0x%x: %v", flags, err))
	}
	if err != nil {
		logf("[ADB] all open attempts failed for %s: %v, fallback to sh -s", slavePath, openErrors)
		unix.Close(masterFd)
		c.startAdbShellFallback()
		return
	}

	// 设置伪 tty 窗口大小
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}
	ws := &unix.Winsize{
		Row: uint16(rows),
		Col: uint16(cols),
	}
	unix.IoctlSetWinsize(masterFd, 0x5414, ws) // TIOCSWINSZ = 0x5414

	master := os.NewFile(uintptr(masterFd), "pty-master")
	slave := os.NewFile(uintptr(slaveFd), "pty-slave")

	// 启动 shell，stdin/stdout/stderr 都连接到 slave
	cmd := exec.Command("sh", "-i")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "HOME=/data/local/tmp")
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	// 设置控制终端：创建新会话 + 使用 slave(fd=0) 作为控制终端
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0, // stdin 就是 slave，文件描述符 0
	}

	if err := cmd.Start(); err != nil {
		logf("[ADB] start shell error: %v", err)
		master.Close()
		slave.Close()
		c.sendAdbData([]byte("[ERROR] failed to start shell\n"))
		return
	}
	// 子进程已继承 slave，关闭父进程的 slave
	slave.Close()

	c.adbShell = cmd
	c.adbStdin = master // 用 master 作为输入输出
	c.adbStdout = master
	c.adbRunning = true

	// 从 master 读取输出（stdout 和 stderr 都在 master 上）
	go func() {
		logf("[ADB] pty read goroutine started")
		buf := make([]byte, 4096)
		for c.adbRunning {
			n, err := master.Read(buf)
			if n > 0 {
				c.sendAdbData(buf[:n])
			}
			if err != nil {
				logf("[ADB] pty read error: %v", err)
				break
			}
		}
		logf("[ADB] pty read goroutine exited")
	}()

	// 等待进程结束
	go func() {
		cmd.Wait()
		logf("[ADB] shell exited")
		c.adbRunning = false
		master.Close()
		c.sendAdbData([]byte("\r\n[Shell] process exited\r\n"))
	}()

	logf("[ADB] interactive shell started with PTY (pid=%d, slave=%s)", cmd.Process.Pid, slavePath)
}

// startAdbShellFallback 无 PTY 时的回退方案
func (c *WebRTCClient) startAdbShellFallback() {
	if c.adbRunning {
		return
	}

	logf("[ADB] starting fallback shell (sh -s)")
	cmd := exec.Command("sh", "-s")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "PS1=$ ", "HOME=/data/local/tmp")

	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		logf("[ADB] start shell error: %v", err)
		c.sendAdbData([]byte("[ERROR] failed to start shell\n"))
		return
	}

	c.adbShell = cmd
	c.adbStdin = stdin
	c.adbStdout = stdout
	c.adbStderr = stderr
	c.adbRunning = true

	go func() {
		buf := make([]byte, 4096)
		for c.adbRunning {
			n, err := stdout.Read(buf)
			if n > 0 {
				c.sendAdbData(buf[:n])
			}
			if err != nil {
				break
			}
		}
	}()
	go func() {
		buf := make([]byte, 4096)
		for c.adbRunning {
			n, err := stderr.Read(buf)
			if n > 0 {
				c.sendAdbData(buf[:n])
			}
			if err != nil {
				break
			}
		}
	}()
	go func() {
		cmd.Wait()
		c.adbRunning = false
	}()

	logf("[ADB] fallback shell started (pid=%d)", cmd.Process.Pid)
}

// sendAdbData 发送终端输出到前端
func (c *WebRTCClient) sendAdbData(data []byte) {
	if c.adbDC == nil {
		logf("[ADB] sendAdbData skipped: adbDC is nil")
		return
	}
	rs := c.adbDC.ReadyState()
	if rs != webrtc.DataChannelStateOpen {
		logf("[ADB] sendAdbData skipped: readyState=%v (want Open), data=%d bytes", rs, len(data))
		return
	}
	logf("[ADB] sendAdbData sending %d bytes: %q", len(data), string(data[:min(64, len(data))]))
	if err := c.adbDC.Send(data); err != nil {
		logf("[ADB] sendAdbData error: %v", err)
	}
}

// onAICommandMessage ai-command-channel：P2P 命令（后续完善）
func (c *WebRTCClient) onAICommandMessage(msg webrtc.DataChannelMessage) {
	logf("[AI-Command] %s", string(msg.Data))
}

// 简单数值工具
func toInt(v interface{}) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		var n int
		fmt.Sscanf(t, "%d", &n)
		return n
	}
	return 0
}

func toFloat(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case string:
		var f float64
		fmt.Sscanf(t, "%f", &f)
		return f
	}
	return 0
}

// audioLoop 从 scrcpy audio channel 读取 Opus 音频包，封装为 RTP 发送
// scrcpy 音频格式（Streamer）：
//   开头：[codecId:4 bytes big-endian]（如果 sendCodecMeta=true）
//   每帧：[ptsAndFlags:8 bytes big-endian][packetSize:4 bytes big-endian][opus data]
// ptsAndFlags: bit63=config flag, bit62=key frame flag, 低62位=pts(微秒)
func (c *WebRTCClient) audioLoop() {
	if c.audioTrack == nil || c.hub.sc.audioConn == nil {
		return
	}
	logf("[Audio] audio loop started")
	defer logf("[Audio] audio loop stopped")

	conn := c.hub.sc.audioConn
	var seq uint16
	var ts uint32
	ssrc := uint32(0x12345678)
	header := make([]byte, 12)
	frameMeta := make([]byte, 12) // 8 bytes ptsAndFlags + 4 bytes packetSize
	syncErrors := 0
	debugCount := 0
	const opusCodecID = 0x6f707573 // "opus" in ASCII

	// 读取开头的 4 字节，判断是否是 codec ID
	first4 := make([]byte, 4)
	if _, err := io.ReadFull(conn, first4); err != nil {
		logf("[Audio] read first 4 bytes error: %v", err)
		return
	}
	first4Val := binary.BigEndian.Uint32(first4)
	logf("[Audio] first 4 bytes: %02x %02x %02x %02x (0x%08x)", first4[0], first4[1], first4[2], first4[3], first4Val)

	if first4Val == opusCodecID {
		// 是 codec ID，正常处理
		logf("[Audio] codec id=opus (0x%08x)", first4Val)
	} else {
		// 不是 codec ID，说明这 4 字节是 frame meta 的一部分
		logf("[Audio] no codec id (got 0x%08x), treating as frame meta part", first4Val)
		// 读取剩余的 8 字节，组合成完整的 12 字节 frame meta
		remaining8 := make([]byte, 8)
		if _, err := io.ReadFull(conn, remaining8); err != nil {
			logf("[Audio] read remaining 8 bytes error: %v", err)
			return
		}
		copy(frameMeta[0:4], first4)
		copy(frameMeta[4:12], remaining8)
		logf("[Audio] first frame meta: % x", frameMeta)
		// 直接处理这个 frame meta
		ptsAndFlags := binary.BigEndian.Uint64(frameMeta[0:8])
		packetSize := binary.BigEndian.Uint32(frameMeta[8:12])
		isConfig := (ptsAndFlags & (1 << 63)) != 0
		logf("[Audio] first frame: ptsAndFlags=0x%016x packetSize=%d isConfig=%v", ptsAndFlags, packetSize, isConfig)

		if packetSize > 0 && packetSize <= 1024*1024 {
			opusData := make([]byte, packetSize)
			if _, err := io.ReadFull(conn, opusData); err == nil && !isConfig {
				header[0] = 0x80
				header[1] = 111
				binary.BigEndian.PutUint16(header[2:4], seq)
				binary.BigEndian.PutUint32(header[4:8], ts)
				binary.BigEndian.PutUint32(header[8:12], ssrc)
				pkt := make([]byte, 12+len(opusData))
				copy(pkt, header)
				copy(pkt[12:], opusData)
				c.audioTrack.Write(pkt)
				seq++
				ts += 960
			}
		}
	}

	for {
		// 读取 12 字节 frame meta: ptsAndFlags(8) + packetSize(4)
		if _, err := io.ReadFull(conn, frameMeta); err != nil {
			if err != io.EOF {
				logf("[Audio] read frame meta error: %v", err)
			}
			return
		}
		ptsAndFlags := binary.BigEndian.Uint64(frameMeta[0:8])
		packetSize := binary.BigEndian.Uint32(frameMeta[8:12])
		isConfig := (ptsAndFlags & (1 << 63)) != 0
		pts := ptsAndFlags & ((1 << 62) - 1)

		// 打印前 5 个 frame meta 的详细信息用于调试
		if debugCount < 5 {
			logf("[Audio] frame #%d: meta=% x ptsAndFlags=0x%016x packetSize=%d isConfig=%v pts=%d", debugCount, frameMeta, ptsAndFlags, packetSize, isConfig, pts)
			debugCount++
		}

		// 验证 packetSize 有效性
		if packetSize == 0 || packetSize > 1024*1024 {
			syncErrors++
			if syncErrors <= 10 || syncErrors%100 == 0 {
				logf("[Audio] invalid packet size=%d (config=%v pts=%d), sync errors=%d, resyncing...", packetSize, isConfig, pts, syncErrors)
			}
			// 数据重新同步：跳过 1 字节，继续寻找有效的 frame meta
			discardBuf := make([]byte, 1)
			if _, err := conn.Read(discardBuf); err != nil {
				logf("[Audio] resync read error: %v", err)
				return
			}
			continue
		}

		// 重置同步错误计数
		if syncErrors > 0 {
			logf("[Audio] resynced successfully after %d errors", syncErrors)
			syncErrors = 0
		}

		// 读取 Opus 数据
		opusData := make([]byte, packetSize)
		if _, err := io.ReadFull(conn, opusData); err != nil {
			logf("[Audio] read opus data error: %v", err)
			return
		}

		// 跳过 config 包（Opus 编解码器配置，浏览器不需要）
		if isConfig {
			continue
		}

		// 检查是否启用音频发送（用户关闭音频时不发送，但保持连接打开，避免重启 scrcpy-server）
		if !c.hub.IsAudioEnabled() {
			// 丢弃音频数据，不发送，但继续读取保持连接
			continue
		}

		// 构建 RTP header
		header[0] = 0x80 // Version=2, Padding=0, Extension=0, CSRC=0
		header[1] = 111  // Payload type=111 (Opus)
		binary.BigEndian.PutUint16(header[2:4], seq)
		binary.BigEndian.PutUint32(header[4:8], ts)
		binary.BigEndian.PutUint32(header[8:12], ssrc)

		// 发送 RTP 包
		pkt := make([]byte, 12+len(opusData))
		copy(pkt, header)
		copy(pkt[12:], opusData)
		if _, err := c.audioTrack.Write(pkt); err != nil {
			logf("[Audio] write rtp error: %v", err)
			return
		}

		seq++
		// Opus 帧通常 20ms = 960 采样 @ 48kHz
		ts += 960
	}
}

// ---- BWE 带宽估计动态码率调整（接近原版实现）----

const (
	bweMinBitrate   = 1_000_000  // 1Mbps 最低码率
	bweMaxBitrate   = 8_000_000  // 8Mbps 最高码率
	bweInitial      = 2_000_000  // 2Mbps 初始码率
	bweAdjustRatio  = 0.2         // 每次最多调整 20%
	bweRTTThreshold = 80.0        // RTT 超过 80ms 开始降码率
	bweInterval     = 2 * time.Second // BWE 调整间隔
)

// bweLoop 定期获取 WebRTC 统计信息，动态调整编码器码率
func (h *WebRTCHub) bweLoop() {
	currentBitrate := bweInitial
	logf("[BWE] started, initial bitrate=%d bps (range %d-%d)", currentBitrate, bweMinBitrate, bweMaxBitrate)

	ticker := time.NewTicker(bweInterval)
	defer ticker.Stop()

	for range ticker.C {
		h.mu.Lock()
		var pc *webrtc.PeerConnection
		for _, c := range h.clients {
			if c.pc != nil {
				pc = c.pc
				break
			}
		}
		h.mu.Unlock()

		if pc == nil {
			continue
		}

		// 获取 ICE candidate pair 统计
		stats := pc.GetStats()
		rtt := 0.0
		availableBitrate := 0.0

		for _, s := range stats {
			if icePair, ok := s.(webrtc.ICECandidatePairStats); ok {
				if icePair.Nominated {
					rtt = icePair.CurrentRoundTripTime * 1000 // 转 ms
					availableBitrate = float64(icePair.AvailableOutgoingBitrate)
				}
			}
		}

		// BWE 决策
		targetBitrate := currentBitrate

		if availableBitrate > 0 {
			// 有可用带宽估计，直接用它作为参考（限制在范围内）
			targetBitrate = int(availableBitrate)
			if targetBitrate < bweMinBitrate {
				targetBitrate = bweMinBitrate
			}
			if targetBitrate > bweMaxBitrate {
				targetBitrate = bweMaxBitrate
			}
		} else {
			// 无可用带宽估计，用 RTT 启发式调整
			if rtt > bweRTTThreshold {
				// 网络差，降码率
				targetBitrate = int(float64(currentBitrate) * (1 - bweAdjustRatio))
			} else if rtt < 30.0 && rtt > 0 {
				// 网络好，升码率
				targetBitrate = int(float64(currentBitrate) * (1 + bweAdjustRatio))
			}
		}

		// 限制调整幅度（每次最多 20%）
		maxDelta := int(float64(currentBitrate) * bweAdjustRatio)
		if targetBitrate > currentBitrate+maxDelta {
			targetBitrate = currentBitrate + maxDelta
		}
		if targetBitrate < currentBitrate-maxDelta {
			targetBitrate = currentBitrate - maxDelta
		}

		// 限制在范围内
		if targetBitrate < bweMinBitrate {
			targetBitrate = bweMinBitrate
		}
		if targetBitrate > bweMaxBitrate {
			targetBitrate = bweMaxBitrate
		}

		// 码率变化超过 5% 才发送，避免频繁调整
		if abs(targetBitrate-currentBitrate) > currentBitrate/20 {
			if err := h.sc.SetBitrate(targetBitrate); err != nil {
				logf("[BWE] set bitrate error: %v", err)
			} else {
				logf("[BWE] adjust bitrate: %d -> %d bps (rtt=%.1fms avail=%.0fkbps)",
					currentBitrate, targetBitrate, rtt, availableBitrate/1000)
				currentBitrate = targetBitrate
			}
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// onCameraMessage camera-channel：相机控制命令
func (c *WebRTCClient) onCameraMessage(msg webrtc.DataChannelMessage) {
	if len(msg.Data) == 0 || msg.Data[0] != '{' {
		return
	}

	var m map[string]interface{}
	if err := json.Unmarshal(msg.Data, &m); err != nil {
		logf("[Camera] parse error: %v", err)
		return
	}

	action, _ := m["action"].(string)
	logf("[Camera] action: %s", action)

	switch action {
	case "switch_camera":
		c.handleSwitchCamera(m)
	case "take_photo":
		c.handleTakePhoto(m)
	case "set_flash":
		c.handleSetFlash(m)
	default:
		logf("[Camera] unknown action: %s", action)
	}
}

func (c *WebRTCClient) sendCameraResponse(resp map[string]interface{}) {
	data, err := json.Marshal(resp)
	if err != nil {
		logf("[Camera] marshal error: %v", err)
		return
	}
	if c.cameraDC != nil {
		c.cameraDC.SendText(string(data))
	}
}

// handleSwitchCamera 切换前后摄像头（通过 shell 命令模拟）
func (c *WebRTCClient) handleSwitchCamera(m map[string]interface{}) {
	camera, _ := m["camera"].(string) // "front" or "back"
	logf("[Camera] switch to: %s", camera)
	// 实际实现需要通过 Camera2 API 或 scrcpy-server 支持
	// 这里先返回成功响应
	c.sendCameraResponse(map[string]interface{}{
		"type":    "camera_result",
		"action":  "switch_camera",
		"camera":  camera,
		"success": true,
	})
}

// handleTakePhoto 拍照
func (c *WebRTCClient) handleTakePhoto(m map[string]interface{}) {
	logf("[Camera] take photo")
	// 使用 screencap 截图作为临时实现（实际应使用 Camera2 API 拍照）
	screenshotPath := "/data/local/tmp/camera_photo.png"
	cmd := exec.Command("screencap", "-p", screenshotPath)
	if err := cmd.Run(); err != nil {
		logf("[Camera] take photo error: %v", err)
		c.sendCameraResponse(map[string]interface{}{
			"type":    "camera_result",
			"action":  "take_photo",
			"error":   err.Error(),
			"success": false,
		})
		return
	}

	data, err := os.ReadFile(screenshotPath)
	if err != nil {
		c.sendCameraResponse(map[string]interface{}{
			"type":    "camera_result",
			"action":  "take_photo",
			"error":   err.Error(),
			"success": false,
		})
		return
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	logf("[Camera] photo taken, size=%d bytes", len(data))
	c.sendCameraResponse(map[string]interface{}{
		"type":    "camera_result",
		"action":  "take_photo",
		"data":    encoded,
		"success": true,
	})
}

// handleSetFlash 设置闪光灯
func (c *WebRTCClient) handleSetFlash(m map[string]interface{}) {
	mode, _ := m["mode"].(string) // "on", "off", "auto"
	logf("[Camera] set flash: %s", mode)
	c.sendCameraResponse(map[string]interface{}{
		"type":    "camera_result",
		"action":  "set_flash",
		"mode":    mode,
		"success": true,
	})
}
