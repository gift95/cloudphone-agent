package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// mockConn 实现 net.Conn 接口，用于测试 VideoReader
type mockConn struct {
	readBuf  *bytes.Buffer
	writeBuf *bytes.Buffer
	closed   bool
	mu       sync.Mutex
}

func newMockConn(data []byte) *mockConn {
	return &mockConn{
		readBuf:  bytes.NewBuffer(data),
		writeBuf: &bytes.Buffer{},
	}
}

func (m *mockConn) Read(b []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, io.EOF
	}
	return m.readBuf.Read(b)
}

func (m *mockConn) Write(b []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeBuf.Write(b)
}

func (m *mockConn) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockConn) LocalAddr() net.Addr                { return &net.TCPAddr{} }
func (m *mockConn) RemoteAddr() net.Addr               { return &net.TCPAddr{} }
func (m *mockConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockConn) SetWriteDeadline(t time.Time) error { return nil }

// buildVideoStream 构建测试用的视频流
// 格式: [64B device meta][12B header][frames...]
// frame: [12B meta: ptsAndFlags(8) size(4)][payload]
func buildVideoStream(deviceName string, codecID uint32, w, h uint32, frames []VideoSample) []byte {
	var buf bytes.Buffer

	// 64B device meta
	meta := make([]byte, 64)
	copy(meta, deviceName)
	buf.Write(meta)

	// 12B header: codec_id(4) w(4) h(4)
	hdr := make([]byte, 12)
	binary.BigEndian.PutUint32(hdr[0:4], codecID)
	binary.BigEndian.PutUint32(hdr[4:8], w)
	binary.BigEndian.PutUint32(hdr[8:12], h)
	buf.Write(hdr)

	// frames
	for _, f := range frames {
		ptsAndFlags := f.PTS
		if f.KeyFrame {
			ptsAndFlags |= PACKET_FLAG_KEY_FRAME
		}
		frameMeta := make([]byte, 12)
		binary.BigEndian.PutUint64(frameMeta[0:8], ptsAndFlags)
		binary.BigEndian.PutUint32(frameMeta[8:12], uint32(len(f.Data)))
		buf.Write(frameMeta)
		buf.Write(f.Data)
	}

	return buf.Bytes()
}

// buildConfigFrame 构建 config 帧（SPS/PPS）
func buildConfigFrame(spspps []byte) VideoSample {
	return VideoSample{
		Data:     spspps,
		KeyFrame: false,
		PTS:      PACKET_FLAG_CONFIG, // config flag 在 ptsAndFlags 高位
	}
}

// TestVideoReaderDeviceMeta 测试设备元信息解析
func TestVideoReaderDeviceMeta(t *testing.T) {
	spspps := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f}
	stream := buildVideoStream("test-device-001", 0x68323634, 864, 1920, []VideoSample{
		buildConfigFrame(spspps),
		{Data: []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x01}, KeyFrame: true, PTS: 1000},
	})

	conn := newMockConn(stream)
	cfg := &AgentConfig{Debug: false}
	srv := &ScrcpyServer{}
	reader := NewVideoReader(conn, cfg, srv)

	var samples []VideoSample
	reader.SetSampleCallback(func(s VideoSample) {
		samples = append(samples, s)
	})

	// Run 在单独 goroutine 中，因为它会阻塞直到 EOF
	done := make(chan struct{})
	go func() {
		reader.Run()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("VideoReader.Run() timed out")
	}

	// 应该收到 1 个样本（config 帧被缓存，不回调）
	if len(samples) != 1 {
		t.Errorf("got %d samples, want 1", len(samples))
	}

	if len(samples) > 0 {
		s := samples[0]
		if !s.KeyFrame {
			t.Error("first sample should be keyframe")
		}
		if s.PTS != 1000 {
			t.Errorf("PTS = %d, want 1000", s.PTS)
		}
		// 关键帧应该包含 SPS/PPS 前缀
		if len(s.Data) < len(spspps)+6 {
			t.Errorf("keyframe data too short: %d", len(s.Data))
		}
		if !bytes.HasPrefix(s.Data, spspps) {
			t.Error("keyframe should be prefixed with SPS/PPS")
		}
	}

	// 验证视频尺寸被正确设置
	vw, vh := srv.GetVideoSize()
	if vw != 864 || vh != 1920 {
		t.Errorf("video size = %dx%d, want 864x1920", vw, vh)
	}
}

// TestVideoReaderDeltaBeforeKeyframe 测试关键帧前置：第一个关键帧前的 delta 帧应被丢弃
func TestVideoReaderDeltaBeforeKeyframe(t *testing.T) {
	spspps := []byte{0x00, 0x00, 0x00, 0x01, 0x67}
	stream := buildVideoStream("test", 0x68323634, 1080, 1920, []VideoSample{
		buildConfigFrame(spspps),
		{Data: []byte{0x01}, KeyFrame: false, PTS: 100}, // delta 帧，应被丢弃
		{Data: []byte{0x02}, KeyFrame: false, PTS: 200}, // delta 帧，应被丢弃
		{Data: []byte{0x03}, KeyFrame: true, PTS: 300},  // 第一个关键帧，应被接收
		{Data: []byte{0x04}, KeyFrame: false, PTS: 400}, // 关键帧后的 delta，应被接收
	})

	conn := newMockConn(stream)
	reader := NewVideoReader(conn, &AgentConfig{}, nil)

	var samples []VideoSample
	reader.SetSampleCallback(func(s VideoSample) {
		samples = append(samples, s)
	})

	done := make(chan struct{})
	go func() {
		reader.Run()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	// 应该收到 2 个样本（第一个关键帧 + 之后的 delta）
	if len(samples) != 2 {
		t.Errorf("got %d samples, want 2 (delta before keyframe should be dropped)", len(samples))
	}
	if len(samples) >= 1 && samples[0].PTS != 300 {
		t.Errorf("first sample PTS = %d, want 300", samples[0].PTS)
	}
	if len(samples) >= 2 && samples[1].PTS != 400 {
		t.Errorf("second sample PTS = %d, want 400", samples[1].PTS)
	}
}

// TestVideoReaderConfigFrameCaching 测试 config 帧缓存
func TestVideoReaderConfigFrameCaching(t *testing.T) {
	spspps := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f, 0x00, 0x00, 0x00, 0x01, 0x68}
	stream := buildVideoStream("test", 0x68323634, 1080, 1920, []VideoSample{
		buildConfigFrame(spspps),
		{Data: []byte{0x65, 0x01, 0x02}, KeyFrame: true, PTS: 1000},
		{Data: []byte{0x65, 0x03, 0x04}, KeyFrame: true, PTS: 2000},
	})

	conn := newMockConn(stream)
	reader := NewVideoReader(conn, &AgentConfig{}, nil)

	var samples []VideoSample
	reader.SetSampleCallback(func(s VideoSample) {
		samples = append(samples, s)
	})

	done := make(chan struct{})
	go func() {
		reader.Run()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	if len(samples) != 2 {
		t.Errorf("got %d samples, want 2", len(samples))
	}

	// 每个关键帧都应该以 SPS/PPS 开头
	for i, s := range samples {
		if !bytes.HasPrefix(s.Data, spspps) {
			t.Errorf("sample %d does not start with SPS/PPS", i)
		}
	}
}

// TestVideoReaderBadFrameSize 测试异常帧大小处理
func TestVideoReaderBadFrameSize(t *testing.T) {
	var buf bytes.Buffer
	// device meta
	buf.Write(make([]byte, 64))
	// header
	hdr := make([]byte, 12)
	binary.BigEndian.PutUint32(hdr[0:4], 0x68323634)
	binary.BigEndian.PutUint32(hdr[4:8], 1080)
	binary.BigEndian.PutUint32(hdr[8:12], 1920)
	buf.Write(hdr)
	// 坏帧：size = 5MB (超过 4MB 限制)
	badFrameMeta := make([]byte, 12)
	binary.BigEndian.PutUint64(badFrameMeta[0:8], 1000)
	binary.BigEndian.PutUint32(badFrameMeta[8:12], 5*1024*1024)
	buf.Write(badFrameMeta)

	conn := newMockConn(buf.Bytes())
	reader := NewVideoReader(conn, &AgentConfig{}, nil)

	sampleCount := 0
	reader.SetSampleCallback(func(s VideoSample) {
		sampleCount++
	})

	done := make(chan struct{})
	go func() {
		reader.Run()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	if sampleCount != 0 {
		t.Errorf("got %d samples, want 0 (bad frame should cause exit)", sampleCount)
	}
}

// TestVideoReaderEmptyStream 测试空流处理
func TestVideoReaderEmptyStream(t *testing.T) {
	conn := newMockConn([]byte{})
	reader := NewVideoReader(conn, &AgentConfig{}, nil)

	sampleCount := 0
	reader.SetSampleCallback(func(s VideoSample) {
		sampleCount++
	})

	done := make(chan struct{})
	go func() {
		reader.Run()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	if sampleCount != 0 {
		t.Errorf("got %d samples, want 0", sampleCount)
	}
}

// TestVideoReaderMarkFrame 测试帧时间标记
func TestVideoReaderMarkFrame(t *testing.T) {
	spspps := []byte{0x67}
	stream := buildVideoStream("test", 0x68323634, 1080, 1920, []VideoSample{
		buildConfigFrame(spspps),
		{Data: []byte{0x65}, KeyFrame: true, PTS: 1000},
	})

	conn := newMockConn(stream)
	srv := &ScrcpyServer{}
	reader := NewVideoReader(conn, &AgentConfig{}, srv)
	reader.SetSampleCallback(func(s VideoSample) {})

	done := make(chan struct{})
	go func() {
		reader.Run()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	// FrameStale 应该返回很小的值（刚收到帧）
	stale := srv.FrameStale()
	if stale > 5*time.Second {
		t.Errorf("frame stale = %v, should be small after receiving frame", stale)
	}
}

// TestPacketFlagConstants 测试包标志常量
func TestPacketFlagConstants(t *testing.T) {
	if PACKET_FLAG_CONFIG != uint64(1)<<63 {
		t.Errorf("PACKET_FLAG_CONFIG = %x, want %x", PACKET_FLAG_CONFIG, uint64(1)<<63)
	}
	if PACKET_FLAG_KEY_FRAME != uint64(1)<<62 {
		t.Errorf("PACKET_FLAG_KEY_FRAME = %x, want %x", PACKET_FLAG_KEY_FRAME, uint64(1)<<62)
	}
}

// TestWaitFor 测试 waitFor 工具函数
func TestWaitFor(t *testing.T) {
	// 立即成功
	start := time.Now()
	result := waitFor("immediate", 1*time.Second, func() bool {
		return true
	})
	if !result {
		t.Error("waitFor should return true for immediate success")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Error("waitFor should return immediately when condition is true")
	}

	// 超时
	start = time.Now()
	result = waitFor("timeout", 200*time.Millisecond, func() bool {
		return false
	})
	if result {
		t.Error("waitFor should return false on timeout")
	}
	elapsed := time.Since(start)
	if elapsed < 200*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Errorf("waitFor timeout elapsed = %v, want ~200ms", elapsed)
	}
}

// TestAtoiDefault 测试 atoiDefault 工具函数
func TestAtoiDefault(t *testing.T) {
	tests := []struct {
		input string
		def   int
		want  int
	}{
		{"123", 0, 123},
		{"0", 10, 0},
		{"-5", 0, -5},
		{"abc", 10, 10},
		{"", 42, 42},
	}
	for _, tt := range tests {
		got := atoiDefault(tt.input, tt.def)
		if got != tt.want {
			t.Errorf("atoiDefault(%q, %d) = %d, want %d", tt.input, tt.def, got, tt.want)
		}
	}
}
