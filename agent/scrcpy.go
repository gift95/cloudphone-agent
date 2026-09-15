package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// ScrcpyServer 管理移植版 scrcpy-server（com.genymobile.scrcpy.CoreService）生命周期
// 四通道 UDS：video/audio/touch/control 各使用独立抽象 socket 名
type ScrcpyServer struct {
	cfg     *AgentConfig
	mu      sync.Mutex
	cmd     *exec.Cmd
	listener map[string]net.Listener

	videoConn   net.Conn
	controlConn net.Conn
	audioConn   net.Conn
	touchConn   net.Conn

	videoReader *VideoReader

	// 防止重复重启
	restarting bool

	videoWidth  int // 视频编码输出宽度（如 864）
	videoHeight int // 视频编码输出高度（如 1920）

	lastFrameTime time.Time // 最近一次收到视频帧的时刻（帧保活检测）
	controlCh chan []byte
	closeOnce sync.Once
}

// GetVideoSize 返回视频编码输出尺寸（触控坐标映射需要与视频尺寸一致）
func (s *ScrcpyServer) GetVideoSize() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.videoWidth, s.videoHeight
}

func (s *ScrcpyServer) setVideoSize(w, h int) {
	s.mu.Lock()
	s.videoWidth, s.videoHeight = w, h
	s.mu.Unlock()
}

func NewScrcpyServer(cfg *AgentConfig) *ScrcpyServer {
	return &ScrcpyServer{
		cfg:       cfg,
		listener:  map[string]net.Listener{},
		controlCh: make(chan []byte, 256),
	}
}

// 组装 CoreService 命令行（与原版一致：app_process 以 shell UID 2000 运行）
// 注意：UDS 模式下不需要 port 参数（四通道使用独立抽象 socket 名）
func (s *ScrcpyServer) commandArgs() []string {
	args := []string{
		"3.3.4", // 客户端版本号，须与 BuildConfig.VERSION_NAME 匹配
		"video=true",
		fmt.Sprintf("audio=%v", s.cfg.Audio),
		"control=true",
		"stay_awake=false", // 与原版一致：不保持屏幕常亮
	}
	if s.cfg.MaxSize > 0 {
		args = append(args, fmt.Sprintf("max_size=%d", s.cfg.MaxSize))
	}
	if s.cfg.MaxFPS > 0 {
		args = append(args, fmt.Sprintf("max_fps=%d", s.cfg.MaxFPS))
	}
	if s.cfg.Bitrate > 0 {
		args = append(args, fmt.Sprintf("video_bit_rate=%d", s.cfg.Bitrate))
	}
	// 视频编码格式 (scrcpy-server 接收名称: h264/h265/av1)
	// VP8/VP9 scrcpy-server 不支持，自动回退到 h264
	if s.cfg.VideoCodec == VideoCodecH265 || s.cfg.VideoCodec == VideoCodecAV1 {
		args = append(args, "video_codec="+s.cfg.VideoCodec.String())
	}
	if s.cfg.VideoCodecOpts != "" {
		args = append(args, "video_codec_options="+s.cfg.VideoCodecOpts)
	}
	if s.cfg.Debug {
		args = append(args, "log_level=verbose")
	} else {
		args = append(args, "log_level=info")
	}
	// 摄像头监控模式参数
	if s.cfg.VideoSource == "camera" {
		args = append(args, "video_source=camera")
		if s.cfg.CameraId != "" {
			args = append(args, "camera_id="+s.cfg.CameraId)
		}
		if s.cfg.CameraSize != "" {
			args = append(args, "camera_size="+s.cfg.CameraSize)
		}
		if s.cfg.CameraFacing != "" {
			args = append(args, "camera_facing="+s.cfg.CameraFacing)
		}
		if s.cfg.CameraFps > 0 {
			args = append(args, fmt.Sprintf("camera_fps=%d", s.cfg.CameraFps))
		}
		if s.cfg.CameraHighSpeed {
			args = append(args, "camera_high_speed=true")
		}
		if s.cfg.CameraAr != "" {
			args = append(args, "camera_ar="+s.cfg.CameraAr)
		}
	}
	return args
}

func (s *ScrcpyServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// UDS multi-channel: each channel uses independent abstract socket name
	// Go "@" prefix maps to Linux abstract namespace, compatible with Android LocalSocket
	udsNames := map[string]string{
		"video":   "@scrcpy_video",
		"audio":   "@scrcpy_audio",
		"touch":   "@scrcpy_touch",
		"control": "@scrcpy_control",
	}
	names := []string{"video", "audio", "touch", "control"}
	for _, name := range names {
		os.Remove(udsNames[name])
		ln, err := net.Listen("unix", udsNames[name])
		if err != nil {
			return fmt.Errorf("listen %s UDS: %w", name, err)
		}
		s.listener[name] = ln
		logf("[Scrcpy] listening %s on UDS %s", name, udsNames[name])
	}

	// Start CoreService (UDS mode)
	env := append(os.Environ(), "CLASSPATH="+s.cfg.JarPath)
	args := append([]string{"/system/bin", "com.genymobile.scrcpy.CoreService"}, s.commandArgs()...)
	logf("[Scrcpy] audio=%v args=%v", s.cfg.Audio, args)
	cmd := exec.Command("app_process", args...)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start CoreService: %w", err)
	}
	s.cmd = cmd
	logf("[Scrcpy] CoreService started pid=%d (UDS mode)", cmd.Process.Pid)

	go s.acceptLoop("video", s.listener["video"], &s.videoConn)
	go s.acceptLoop("audio", s.listener["audio"], &s.audioConn)
	go s.acceptLoop("touch", s.listener["touch"], &s.touchConn)
	go s.acceptLoop("control", s.listener["control"], &s.controlConn)
	return nil
}

func (s *ScrcpyServer) acceptLoop(name string, ln net.Listener, connPtr *net.Conn) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		logf("[Scrcpy] %s channel connected from %s", name, c.RemoteAddr())
		if connPtr != nil && *connPtr != nil {
			(*connPtr).Close()
		}
		*connPtr = c
		if name == "video" {
			s.videoReader = NewVideoReader(c, s.cfg, s)
			go s.videoReader.Run()
		}
		if name == "control" {
			go s.readControl(c)
		}
	}
}

// readControl 读取 CoreService 经 control 通道返回的设备消息（剪贴板 ACK 等）
func (s *ScrcpyServer) readControl(c net.Conn) {
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if err != nil {
			logf("[Scrcpy] control channel closed: %v", err)
			return
		}
		if n > 0 {
			logf("[Scrcpy] control recv %d bytes (type=%d)", n, buf[0])
		}
	}
}

func (s *ScrcpyServer) WriteControl(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.controlConn == nil {
		return fmt.Errorf("control channel not connected")
	}
	_, err := s.controlConn.Write(b)
	return err
}

// MarkFrame 记录收到视频帧的时刻（由 VideoReader 调用）
func (s *ScrcpyServer) MarkFrame() {
	s.mu.Lock()
	s.lastFrameTime = time.Now()
	s.mu.Unlock()
}

// FrameStale 返回自最近一帧以来的时长
func (s *ScrcpyServer) FrameStale() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastFrameTime.IsZero() {
		return time.Hour
	}
	return time.Since(s.lastFrameTime)
}

// ResetVideo 请求 CoreService 重置视频编码器（强制输出配置帧+关键帧）
func (s *ScrcpyServer) ResetVideo() error {
	logf("[Scrcpy] send reset_video (force keyframe)")
	return s.WriteControl(EncodeResetVideo())
}

// SetBitrate BWE 动态调整编码器码率（不重启编码器，无黑屏）
func (s *ScrcpyServer) SetBitrate(bitrate int) error {
	logf("[Scrcpy] BWE set bitrate=%d bps", bitrate)
	return s.WriteControl(EncodeSetBitrate(bitrate))
}

// SwitchCamera 动态切换摄像头（不重启 scrcpy-server，通过控制消息切换）
func (s *ScrcpyServer) SwitchCamera(cameraId string) error {
	logf("[Scrcpy] switch camera to %s (dynamic, no restart)", cameraId)
	return s.WriteControl(EncodeSwitchCamera(cameraId))
}

func (s *ScrcpyServer) Stop() {
	s.closeOnce.Do(func() {
		if s.cmd != nil && s.cmd.Process != nil {
			s.cmd.Process.Kill()
		}
		for _, ln := range s.listener {
			if ln != nil {
				ln.Close()
			}
		}
		for _, c := range []net.Conn{s.videoConn, s.audioConn, s.touchConn, s.controlConn} {
			if c != nil {
				c.Close()
			}
		}
	})
}

// StopAudio 仅关闭音频连接，触发 scrcpy-server 音频编码器停止，释放音频设备
// 用于用户在前端关闭音频时，让手机恢复正常声音输出
func (s *ScrcpyServer) StopAudio() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.audioConn != nil {
		logf("[Scrcpy] closing audio connection to release audio device")
		s.audioConn.Close()
		s.audioConn = nil
	}
}

// IsAudioRunning 检查音频连接是否还在运行
func (s *ScrcpyServer) IsAudioRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.audioConn != nil
}

// Restart 重启 scrcpy-server（用于音频被停止后恢复）
// 会杀掉当前进程，关闭所有连接和监听器，然后重新启动
func (s *ScrcpyServer) Restart() error {
	// 防止重复重启
	s.mu.Lock()
	if s.restarting {
		s.mu.Unlock()
		logf("[Scrcpy] restart already in progress, skipping")
		return nil
	}
	s.restarting = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.restarting = false
		s.mu.Unlock()
	}()

	logf("[Scrcpy] restarting...")

	// 1. 杀掉当前进程
	s.mu.Lock()
	if s.cmd != nil && s.cmd.Process != nil {
		logf("[Scrcpy] killing old process pid=%d", s.cmd.Process.Pid)
		s.cmd.Process.Kill()
		s.cmd = nil
	}

	// 2. 关闭所有连接
	if s.videoConn != nil {
		s.videoConn.Close()
		s.videoConn = nil
	}
	if s.audioConn != nil {
		s.audioConn.Close()
		s.audioConn = nil
	}
	if s.touchConn != nil {
		s.touchConn.Close()
		s.touchConn = nil
	}
	if s.controlConn != nil {
		s.controlConn.Close()
		s.controlConn = nil
	}

	// 3. 关闭所有监听器
	for name, ln := range s.listener {
		if ln != nil {
			logf("[Scrcpy] closing listener %s", name)
			ln.Close()
		}
	}
	s.listener = map[string]net.Listener{}
	s.videoReader = nil
	s.videoWidth = 0
	s.videoHeight = 0
	s.lastFrameTime = time.Time{}
	s.mu.Unlock()

	// 4. 等待一下，确保端口释放
	logf("[Scrcpy] waiting for ports to be released...")
	time.Sleep(2 * time.Second)

	// 5. 确保 jar 文件存在（可能在运行过程中被系统清理了）
	logf("[Scrcpy] ensuring libsys_core.so exists...")
	if err := ensureScrcpyServerJar(s.cfg.JarPath); err != nil {
		logf("[Scrcpy] failed to ensure libsys_core.so: %v", err)
		return err
	}

	// 6. 重新启动
	logf("[Scrcpy] starting new scrcpy-server...")
	if err := s.Start(); err != nil {
		logf("[Scrcpy] restart failed: %v", err)
		return err
	}

	logf("[Scrcpy] restarted successfully, waiting for channels to connect...")

	// 7. 等待通道连接（最多等待 10 秒）
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		s.mu.Lock()
		videoOK := s.videoConn != nil
		audioOK := s.audioConn != nil
		controlOK := s.controlConn != nil
		s.mu.Unlock()
		if videoOK && audioOK && controlOK {
			logf("[Scrcpy] all channels connected after %dms", (i+1)*500)
			break
		}
		if i%4 == 0 {
			logf("[Scrcpy] waiting for channels: video=%v audio=%v control=%v", videoOK, audioOK, controlOK)
		}
	}

	logf("[Scrcpy] restart completed")
	return nil
}

// ---------------------------------------------------------------------------
// 视频流解析：scrcpy 协议
//   [64B device meta][12B header: codec_id(4) w(4) h(4)]
//   帧: [12B meta: ptsAndFlags(8) size(4)][payload]
//   PACKET_FLAG_CONFIG=1<<63  PACKET_FLAG_KEY_FRAME=1<<62
// ---------------------------------------------------------------------------
const (
	PACKET_FLAG_CONFIG    uint64 = 1 << 63
	PACKET_FLAG_KEY_FRAME uint64 = 1 << 62
)

type VideoSample struct {
	Data     []byte // Annex-B H264（关键帧含 SPS/PPS）
	KeyFrame bool
	PTS      uint64
}

type VideoReader struct {
	conn net.Conn
	cfg  *AgentConfig
	srv  *ScrcpyServer

	spsPPS []byte // 缓存 SPS/PPS（config 帧）
	onSample func(VideoSample)
	debug    bool
}

func NewVideoReader(conn net.Conn, cfg *AgentConfig, srv *ScrcpyServer) *VideoReader {
	return &VideoReader{conn: conn, cfg: cfg, srv: srv, debug: cfg.Debug}
}

func (r *VideoReader) SetSampleCallback(cb func(VideoSample)) {
	r.onSample = cb
}

func (r *VideoReader) Run() {
	// 1. 64B device meta
	meta := make([]byte, 64)
	if _, err := io.ReadFull(r.conn, meta); err != nil {
		logf("[Video] read device meta: %v", err)
		return
	}
	name := meta[:]
	for i, b := range name {
		if b == 0 {
			name = name[:i]
			break
		}
	}
	logf("[Video] device: %s", string(name))

	// 2. 12B header
	hdr := make([]byte, 12)
	if _, err := io.ReadFull(r.conn, hdr); err != nil {
		logf("[Video] read header: %v", err)
		return
	}
	codecID := binary.BigEndian.Uint32(hdr[0:4])
	w := binary.BigEndian.Uint32(hdr[4:8])
	h := binary.BigEndian.Uint32(hdr[8:12])
	logf("[Video] codec=%d(%s) %dx%d", codecID, string(hdr[0:4]), w, h)
	if r.srv != nil {
		r.srv.setVideoSize(int(w), int(h)) // 保存视频编码尺寸，供触控坐标映射使用
	}

	// 3. 帧循环
	var ptsBuf [12]byte
	gotKeyframe := false // 关键帧前置：收到第一个关键帧前丢弃 delta 帧，避免浏览器无法解码
	for {
		if _, err := io.ReadFull(r.conn, ptsBuf[:]); err != nil {
			logf("[Video] read frame meta: %v", err)
			return
		}
		ptsAndFlags := binary.BigEndian.Uint64(ptsBuf[0:8])
		size := int64(binary.BigEndian.Uint32(ptsBuf[8:12]))
		if size < 0 || size > 4*1024*1024 {
			logf("[Video] bad frame size %d", size)
			return
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(r.conn, payload); err != nil {
			logf("[Video] read payload: %v", err)
			return
		}
		config := ptsAndFlags&PACKET_FLAG_CONFIG != 0
		key := ptsAndFlags&PACKET_FLAG_KEY_FRAME != 0
		pts := ptsAndFlags & 0x3FFFFFFFFFFFFFFF

		if config {
			// 缓存 SPS/PPS
			r.spsPPS = append([]byte(nil), payload...)
			if r.debug {
				hd := payload
				if len(hd) > 16 {
					hd = hd[:16]
				}
				logf("[Video] config frame %d bytes SPS/PPS cached: %x", len(payload), hd)
			}
			continue
		}
		if r.onSample != nil {
			// 关键帧前置：未收到第一个关键帧前丢弃 delta 帧
			if !key && !gotKeyframe {
				continue
			}
			if key {
				gotKeyframe = true
			}
			var data []byte
			if key && len(r.spsPPS) > 0 {
				data = make([]byte, 0, len(r.spsPPS)+len(payload))
				data = append(data, r.spsPPS...)
				data = append(data, payload...)
			} else {
				data = payload
			}
			if r.srv != nil {
				r.srv.MarkFrame()
			}
			if r.debug && key {
				hd := data
				if len(hd) > 16 {
					hd = hd[:16]
				}
				logf("[Video] keyframe %d bytes first16=%x", len(data), hd)
			}
			r.onSample(VideoSample{Data: data, KeyFrame: key, PTS: pts})
		}
	}
}

// 工具：等待文件存在
func waitFor(what string, timeout time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	logf("waitFor(%s) timeout", what)
	return false
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
