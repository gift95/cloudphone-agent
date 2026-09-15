package main

import (
	"fmt"
	"strings"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

// ---------------------------------------------------------------------------
// 视频编码格式管理
//   - 支持 H.264 (默认，兼容性最好)
//   - H.265/HEVC (实验性，需浏览器和设备同时支持)
//   - AV1 (实验性，新一代编码，码率降低 30-50%)
//
// 注意：Pion WebRTC v3 对 H.265/AV1 的支持需要：
//   1. scrcpy-server 端 MediaCodec 支持对应编码格式
//   2. 浏览器端支持对应解码 (WebCodecs / MSE)
//   3. SDP 协商时双方都支持对应 codec
// ---------------------------------------------------------------------------

// VideoCodec 视频编码格式
type VideoCodec string

const (
	VideoCodecH264  VideoCodec = "h264"  // H.264/AVC (默认，广泛支持)
	VideoCodecH265  VideoCodec = "h265"  // H.265/HEVC (实验性)
	VideoCodecAV1   VideoCodec = "av1"   // AV1 (实验性，新一代编码)
	VideoCodecVP8   VideoCodec = "vp8"   // VP8 (WebRTC 原生支持)
	VideoCodecVP9   VideoCodec = "vp9"   // VP9 (WebRTC 原生支持)
)

// ParseVideoCodec 解析视频编码格式字符串
func ParseVideoCodec(s string) (VideoCodec, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "h264", "h.264", "avc":
		return VideoCodecH264, nil
	case "h265", "h.265", "hevc":
		return VideoCodecH265, nil
	case "av1":
		return VideoCodecAV1, nil
	case "vp8":
		return VideoCodecVP8, nil
	case "vp9":
		return VideoCodecVP9, nil
	default:
		return VideoCodecH264, fmt.Errorf("unknown video codec: %q, using h264 as default", s)
	}
}

// String 返回编码格式字符串
func (c VideoCodec) String() string {
	return string(c)
}

// MimeType 返回 WebRTC MIME 类型
func (c VideoCodec) MimeType() string {
	switch c {
	case VideoCodecH264:
		return webrtc.MimeTypeH264
	case VideoCodecH265:
		return "video/H265"
	case VideoCodecAV1:
		return "video/AV1"
	case VideoCodecVP8:
		return webrtc.MimeTypeVP8
	case VideoCodecVP9:
		return webrtc.MimeTypeVP9
	default:
		return webrtc.MimeTypeH264
	}
}

// ScrcpyCodecID 返回 scrcpy-server 对应的 codec_id
// scrcpy 协议: 0=h264, 1=h265, 2=av1 (参考 scrcpy v3.x VideoCodec 枚举)
func (c VideoCodec) ScrcpyCodecID() int {
	switch c {
	case VideoCodecH264:
		return 0
	case VideoCodecH265:
		return 1
	case VideoCodecAV1:
		return 2
	default:
		return 0
	}
}

// IsExperimental 是否为实验性格式
func (c VideoCodec) IsExperimental() bool {
	return c == VideoCodecH265 || c == VideoCodecAV1
}

// RecommendedBitrate 返回推荐码率 (bps)
// AV1/H.265 可比 H.264 降低 30-50% 码率获得相同画质
func (c VideoCodec) RecommendedBitrate(resolution string, fps int) int {
	// 基础码率估算 (H.264, 1080p30 ~ 4Mbps)
	base := 4_000_000
	switch resolution {
	case "720p", "1280x720":
		base = 2_000_000
	case "1080p", "1920x1080":
		base = 4_000_000
	case "1440p", "2560x1440":
		base = 8_000_000
	case "4k", "2160p", "3840x2160":
		base = 16_000_000
	}
	// 帧率调整 (60fps 需要 ~1.5x 码率)
	if fps > 30 {
		base = base * fps / 30
	}
	// 编码格式调整
	switch c {
	case VideoCodecH265:
		base = base * 70 / 100 // H.265 节省 ~30%
	case VideoCodecAV1:
		base = base * 55 / 100 // AV1 节省 ~45%
	case VideoCodecVP9:
		base = base * 65 / 100 // VP9 节省 ~35%
	}
	return base
}

// ---------------------------------------------------------------------------
// VideoTrackFactory 视频 Track 工厂
//   根据编码格式创建对应的 TrackLocalStaticSample
// ---------------------------------------------------------------------------

// VideoTrackFactory 视频轨道工厂
type VideoTrackFactory struct {
	codec VideoCodec
}

// NewVideoTrackFactory 创建视频轨道工厂
func NewVideoTrackFactory(codec VideoCodec) *VideoTrackFactory {
	return &VideoTrackFactory{codec: codec}
}

// CreateTrack 创建视频轨道
func (f *VideoTrackFactory) CreateTrack() (*webrtc.TrackLocalStaticSample, error) {
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: f.codec.MimeType()},
		"video",
		"cloudphone",
	)
	if err != nil {
		return nil, fmt.Errorf("create %s track: %w", f.codec, err)
	}
	logf("[Codec] created video track: %s (mime=%s, experimental=%v)",
		f.codec, f.codec.MimeType(), f.codec.IsExperimental())
	return track, nil
}

// Codec 返回当前编码格式
func (f *VideoTrackFactory) Codec() VideoCodec {
	return f.codec
}

// ---------------------------------------------------------------------------
// Codec 协商辅助
// ---------------------------------------------------------------------------

// SupportedCodecs 返回当前支持的编码格式列表（按优先级排序）
func SupportedCodecs() []VideoCodec {
	return []VideoCodec{
		VideoCodecH264, // 默认，兼容性最好
		VideoCodecVP8,  // WebRTC 原生支持
		VideoCodecVP9,  // WebRTC 原生支持
		VideoCodecH265, // 实验性
		VideoCodecAV1,  // 实验性
	}
}

// IsCodecSupportedByBrowser 检查浏览器是否支持指定编码格式（基于 WebCodecs API）
// 注意：这是一个辅助函数，实际协商通过 SDP 完成
func IsCodecSupportedByBrowser(codec VideoCodec) bool {
	// 浏览器兼容性参考 (2024+):
	// H.264: 所有现代浏览器
	// VP8/VP9: Chrome/Firefox/Edge
	// H.265: Safari 17+, Chrome (需 flag)
	// AV1: Chrome 70+, Firefox 67+, Safari 17+
	switch codec {
	case VideoCodecH264, VideoCodecVP8, VideoCodecVP9:
		return true
	case VideoCodecH265, VideoCodecAV1:
		// 实验性格式，假设支持（实际通过 SDP 协商）
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// 音频编码格式
// ---------------------------------------------------------------------------

// AudioCodec 音频编码格式
type AudioCodec string

const (
	AudioCodecOpus AudioCodec = "opus" // Opus (默认，WebRTC 标准)
	AudioCodecAAC  AudioCodec = "aac"  // AAC (需转码)
	AudioCodecPCM  AudioCodec = "pcm"  // PCM (原始音频)
)

// ParseAudioCodec 解析音频编码格式
func ParseAudioCodec(s string) (AudioCodec, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "opus":
		return AudioCodecOpus, nil
	case "aac":
		return AudioCodecAAC, nil
	case "pcm":
		return AudioCodecPCM, nil
	default:
		return AudioCodecOpus, fmt.Errorf("unknown audio codec: %q, using opus as default", s)
	}
}

// MimeType 返回音频 MIME 类型
func (c AudioCodec) MimeType() string {
	switch c {
	case AudioCodecOpus:
		return webrtc.MimeTypeOpus
	case AudioCodecAAC:
		return "audio/AAC"
	case AudioCodecPCM:
		return "audio/PCM"
	default:
		return webrtc.MimeTypeOpus
	}
}

// CreateAudioTrack 创建音频轨道
func CreateAudioTrack(codec AudioCodec) (*webrtc.TrackLocalStaticRTP, error) {
	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: codec.MimeType()},
		"audio",
		"cloudphone",
	)
	if err != nil {
		return nil, fmt.Errorf("create %s audio track: %w", codec, err)
	}
	return track, nil
}

// ---------------------------------------------------------------------------
// WebRTC MediaEngine 注册辅助
// ---------------------------------------------------------------------------

// RTPCodecParams 返回指定视频编码格式的 WebRTC RTP codec 参数
// 用于 MediaEngine.RegisterCodec 和 TrackLocalStaticSample 创建
func (c VideoCodec) RTPCodecParams() webrtc.RTPCodecParameters {
	switch c {
	case VideoCodecH264:
		return webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:    webrtc.MimeTypeH264,
				ClockRate:   90000,
				SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
			},
			PayloadType: 102,
		}
	case VideoCodecH265:
		return webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:    "video/H265",
				ClockRate:   90000,
				SDPFmtpLine: "profile-id=1;level-id=120;interop-constraints=1",
			},
			PayloadType: 103,
		}
	case VideoCodecAV1:
		return webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:    "video/AV1",
				ClockRate:   90000,
				SDPFmtpLine: "profile=0;level=5;tier=0",
			},
			PayloadType: 104,
		}
	case VideoCodecVP8:
		return webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypeVP8,
				ClockRate: 90000,
			},
			PayloadType: 96,
		}
	case VideoCodecVP9:
		return webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:    webrtc.MimeTypeVP9,
				ClockRate:   90000,
				SDPFmtpLine: "profile-id=0",
			},
			PayloadType: 98,
		}
	default:
		// fallback to H264
		return VideoCodecH264.RTPCodecParams()
	}
}

// AudioRTPCodecParams 返回指定音频编码格式的 WebRTC RTP codec 参数
func (c AudioCodec) RTPCodecParams() webrtc.RTPCodecParameters {
	switch c {
	case AudioCodecOpus:
		return webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:    webrtc.MimeTypeOpus,
				ClockRate:   48000,
				Channels:    2,
				SDPFmtpLine: "minptime=10;useinbandfec=1",
			},
			PayloadType: 111,
		}
	case AudioCodecAAC:
		return webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:  "audio/AAC",
				ClockRate: 48000,
				Channels:  2,
			},
			PayloadType: 112,
		}
	case AudioCodecPCM:
		return webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:  "audio/PCMU",
				ClockRate: 8000,
				Channels:  1,
			},
			PayloadType: 0,
		}
	default:
		return AudioCodecOpus.RTPCodecParams()
	}
}

// 确保 media 包被引用（用于后续 sample 写入）
var _ = media.Sample{}
