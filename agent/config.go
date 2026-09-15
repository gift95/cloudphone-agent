package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// AgentConfig 命令行参数 + 环境变量（兼容原版 run.sh/run.bat 与 CP_AGENT_* 变量）
type AgentConfig struct {
	Signaling      string // ws://host:port/register_agent
	DeviceID       string
	JarPath        string // scrcpy-server 包路径（原版 libsys_core.so）
	ExternalAddr   string // NAT 外部地址（NAT1To1IP）
	WebRTCPort     int    // Pion UDP mux 端口
	ICEServers     string // 逗号分隔
	Resolution     string // WxH
	MaxSize        int
	MaxFPS         int
	Bitrate        int
	MaxBitrate     int
	VideoCodecOpts string
	SnapshotSec    int
	Audio          bool
	Root           bool
	Debug          bool
	// 编码格式配置
	VideoCodecStr  string // "h264" / "h265" / "av1" / "vp8" / "vp9"
	AudioCodecStr  string // "opus" / "aac" / "pcm"
	VideoCodec     VideoCodec
	AudioCodec     AudioCodec
	// 摄像头监控模式配置（运行时由 scrcpy_options 动态设置）
	VideoSource     string // "display" or "camera"
	CameraId        string
	CameraSize      string // e.g., "1920x1080"
	CameraFacing    string // "front", "back", "external"
	CameraFps       int
	CameraHighSpeed bool
	CameraAr        string // aspect ratio, e.g., "4:3", "16:9", "sensor"
	Timezone        string // 日志时区，如 "Asia/Shanghai", "UTC", "America/New_York"
}

func parseConfig() *AgentConfig {
	cfg := &AgentConfig{}
	flag.StringVar(&cfg.Signaling, "signaling", "", "signaling server ws url")
	flag.StringVar(&cfg.DeviceID, "id", "", "device id")
	flag.StringVar(&cfg.JarPath, "jar", "/data/local/tmp/libsys_core.so", "scrcpy-server jar path")
	flag.StringVar(&cfg.ExternalAddr, "external-addr", "", "external IP for NAT1To1IP")
	flag.IntVar(&cfg.WebRTCPort, "webrtc-port", 50000, "WebRTC UDP port")
	flag.StringVar(&cfg.ICEServers, "ice-servers", "", "comma separated ice servers")
	flag.StringVar(&cfg.Resolution, "resolution", "", "WxH")
	flag.IntVar(&cfg.MaxSize, "max-size", 0, "max video size (0=unlimited, 与原版一致)")
	flag.IntVar(&cfg.MaxFPS, "max-fps", 0, "max video fps (0=unlimited, 与原版一致)")
	flag.IntVar(&cfg.Bitrate, "bitrate", 0, "target bitrate bps")
	flag.IntVar(&cfg.MaxBitrate, "max-bitrate", 0, "max bitrate bps")
	flag.StringVar(&cfg.VideoCodecOpts, "video-codec-options", "", "scrcpy video_codec_options（空=用编码器默认参数，与原版一致）")
	flag.IntVar(&cfg.SnapshotSec, "snapshot-interval", 0, "snapshot interval seconds (0=off)")
	flag.BoolVar(&cfg.Audio, "audio", true, "enable audio")
	flag.BoolVar(&cfg.Root, "root", false, "run as root")
	flag.BoolVar(&cfg.Debug, "debug", false, "debug logging")
	flag.StringVar(&cfg.VideoCodecStr, "video-codec", envOr("CP_AGENT_VIDEO_CODEC", "h264"), "video codec (h264/h265/av1/vp8/vp9)")
	flag.StringVar(&cfg.AudioCodecStr, "audio-codec", envOr("CP_AGENT_AUDIO_CODEC", "opus"), "audio codec (opus/aac/pcm)")
	flag.StringVar(&cfg.Timezone, "timezone", "", "log timezone (e.g., Asia/Shanghai, UTC, America/New_York)")
	flag.Parse()

	// 解析编码格式
	vc, err := ParseVideoCodec(cfg.VideoCodecStr)
	if err != nil {
		logf("[Config] %v", err)
	}
	cfg.VideoCodec = vc
	ac, err := ParseAudioCodec(cfg.AudioCodecStr)
	if err != nil {
		logf("[Config] %v", err)
	}
	cfg.AudioCodec = ac

	// 环境变量覆盖
	if v := os.Getenv("CP_AGENT_SIGNALING"); v != "" {
		cfg.Signaling = v
	}
	if v := os.Getenv("CP_AGENT_ID"); v != "" {
		cfg.DeviceID = v
	}
	if v := os.Getenv("CP_AGENT_JAR"); v != "" {
		cfg.JarPath = v
	}
	if v := os.Getenv("CP_AGENT_EXTERNAL_ADDR"); v != "" {
		cfg.ExternalAddr = v
	}
	if v := os.Getenv("CP_AGENT_WEBRTC_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.WebRTCPort = n
		}
	}
	if v := os.Getenv("CP_AGENT_ICE_SERVERS"); v != "" {
		cfg.ICEServers = v
	}
	if v := os.Getenv("CP_AGENT_TIMEZONE"); v != "" {
		cfg.Timezone = v
	}

	if cfg.Signaling == "" {
		fmt.Fprintln(os.Stderr, "ERROR: -signaling is required")
		os.Exit(1)
	}
	if cfg.DeviceID == "" {
		fmt.Fprintln(os.Stderr, "ERROR: -id is required")
		os.Exit(1)
	}
	return cfg
}

func (c *AgentConfig) resolutionWH() (w, h int) {
	w, h = 1080, 2400
	if c.Resolution != "" {
		parts := strings.SplitN(c.Resolution, "x", 2)
		if len(parts) == 2 {
			if ww, err := strconv.Atoi(parts[0]); err == nil {
				w = ww
			}
			if hh, err := strconv.Atoi(parts[1]); err == nil {
				h = hh
			}
		}
	}
	return
}

func (c *AgentConfig) iceServerList() []string {
	if c.ICEServers == "" {
		return nil
	}
	return strings.Split(c.ICEServers, ",")
}

// envOr 读取环境变量，不存在时返回默认值
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
