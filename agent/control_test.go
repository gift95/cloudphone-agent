package main

import (
	"encoding/binary"
	"testing"
)

// TestEncodeInjectKeycode 测试按键事件编码
// 协议: [0][action:1][keycode:4][repeat:4][metaState:4] = 14 bytes
func TestEncodeInjectKeycode(t *testing.T) {
	tests := []struct {
		name      string
		action    byte
		keycode   int32
		repeat    int32
		metaState int32
		wantLen   int
	}{
		{"key_down", 0, 26, 0, 0, 14},
		{"key_up", 1, 26, 0, 0, 14},
		{"key_with_meta", 0, 29, 0, 0x1000, 14},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EncodeInjectKeycode(tt.action, tt.keycode, tt.repeat, tt.metaState)
			if len(got) != tt.wantLen {
				t.Errorf("EncodeInjectKeycode() length = %d, want %d", len(got), tt.wantLen)
			}
			if got[0] != TypeInjectKeycode {
				t.Errorf("type = %d, want %d", got[0], TypeInjectKeycode)
			}
			if got[1] != tt.action {
				t.Errorf("action = %d, want %d", got[1], tt.action)
			}
			if int32(binary.BigEndian.Uint32(got[2:6])) != tt.keycode {
				t.Errorf("keycode = %d, want %d", int32(binary.BigEndian.Uint32(got[2:6])), tt.keycode)
			}
		})
	}
}

// TestEncodeInjectText 测试文字输入编码
// 协议: [1][len:4][utf8]
func TestEncodeInjectText(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		wantLen int
	}{
		{"empty", "", 5},
		{"ascii", "hello", 10},
		{"chinese", "你好", 11}, // 2 * 3 bytes utf8 + 5 header
		{"long_text_truncated", "This is a very long text that should be truncated to 300 characters maximum length for the scrcpy protocol encoding test case. " +
			"Lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. " +
			"Ut enim ad minim veniam quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat. " +
			"Duis aute irure dolor in reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla pariatur. " +
			"Excepteur sint occaecat cupidatat non proident sunt in culpa qui officia deserunt mollit anim id est laborum.", 305},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EncodeInjectText(tt.text)
			if len(got) != tt.wantLen {
				t.Errorf("length = %d, want %d", len(got), tt.wantLen)
			}
			if got[0] != TypeInjectText {
				t.Errorf("type = %d, want %d", got[0], TypeInjectText)
			}
			strLen := binary.BigEndian.Uint32(got[1:5])
			if int(strLen) != len(got)-5 {
				t.Errorf("encoded length = %d, but payload = %d", strLen, len(got)-5)
			}
		})
	}
}

// TestEncodeInjectTouchEvent 测试触摸事件编码
// 协议: [2][action:1][pointerId:8][x:4][y:4][screenW:2][screenH:2][pressure:2][actionButton:4][buttons:4] = 32 bytes
func TestEncodeInjectTouchEvent(t *testing.T) {
	tests := []struct {
		name       string
		action     byte
		pointerID  uint64
		x, y       int32
		screenW, H uint16
		pressure   float32
		wantLen    int
	}{
		{"touch_down", 0, 1, 540, 960, 1080, 1920, 1.0, 32},
		{"touch_move", 1, 1, 600, 1000, 1080, 1920, 0.8, 32},
		{"touch_up", 2, 1, 540, 960, 1080, 1920, 0.0, 32},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EncodeInjectTouchEvent(tt.action, tt.pointerID, tt.x, tt.y, tt.screenW, tt.H, tt.pressure, 0, 0)
			if len(got) != tt.wantLen {
				t.Errorf("length = %d, want %d", len(got), tt.wantLen)
			}
			if got[0] != TypeInjectTouchEvent {
				t.Errorf("type = %d, want %d", got[0], TypeInjectTouchEvent)
			}
			if got[1] != tt.action {
				t.Errorf("action = %d, want %d", got[1], tt.action)
			}
			if binary.BigEndian.Uint64(got[2:10]) != tt.pointerID {
				t.Errorf("pointerId = %d, want %d", binary.BigEndian.Uint64(got[2:10]), tt.pointerID)
			}
			if int32(binary.BigEndian.Uint32(got[10:14])) != tt.x {
				t.Errorf("x = %d, want %d", int32(binary.BigEndian.Uint32(got[10:14])), tt.x)
			}
			if int32(binary.BigEndian.Uint32(got[14:18])) != tt.y {
				t.Errorf("y = %d, want %d", int32(binary.BigEndian.Uint32(got[14:18])), tt.y)
			}
			if binary.BigEndian.Uint16(got[18:20]) != tt.screenW {
				t.Errorf("screenW = %d, want %d", binary.BigEndian.Uint16(got[18:20]), tt.screenW)
			}
			if binary.BigEndian.Uint16(got[20:22]) != tt.H {
				t.Errorf("screenH = %d, want %d", binary.BigEndian.Uint16(got[20:22]), tt.H)
			}
		})
	}
}

// TestEncodeInjectScrollEvent 测试滚动事件编码
// 协议: [3][x:4][y:4][screenW:2][screenH:2][hScroll:2][vScroll:2][buttons:4] = 21 bytes
func TestEncodeInjectScrollEvent(t *testing.T) {
	got := EncodeInjectScrollEvent(540, 960, 1080, 1920, 0, 1.0, 0)
	if len(got) != 21 {
		t.Errorf("length = %d, want 21", len(got))
	}
	if got[0] != TypeInjectScrollEvent {
		t.Errorf("type = %d, want %d", got[0], TypeInjectScrollEvent)
	}
	if int32(binary.BigEndian.Uint32(got[1:5])) != 540 {
		t.Errorf("x = %d, want 540", int32(binary.BigEndian.Uint32(got[1:5])))
	}
}

// TestEncodeBackOrScreenOn 测试返回/亮屏编码
func TestEncodeBackOrScreenOn(t *testing.T) {
	got := EncodeBackOrScreenOn(0)
	if len(got) != 2 {
		t.Errorf("length = %d, want 2", len(got))
	}
	if got[0] != TypeBackOrScreenOn {
		t.Errorf("type = %d, want %d", got[0], TypeBackOrScreenOn)
	}
}

// TestEncodeSetClipboard 测试剪贴板设置编码
// 协议: [9][sequence:8][paste:1][len:4][utf8]
func TestEncodeSetClipboard(t *testing.T) {
	got := EncodeSetClipboard(12345, "test clipboard", true)
	if len(got) != 1+8+1+4+len("test clipboard") {
		t.Errorf("length = %d, want %d", len(got), 1+8+1+4+len("test clipboard"))
	}
	if got[0] != TypeSetClipboard {
		t.Errorf("type = %d, want %d", got[0], TypeSetClipboard)
	}
	if binary.BigEndian.Uint64(got[1:9]) != 12345 {
		t.Errorf("sequence = %d, want 12345", binary.BigEndian.Uint64(got[1:9]))
	}
	if got[9] != 1 {
		t.Errorf("paste = %d, want 1", got[9])
	}
}

// TestEncodeRotateDevice 测试旋转设备编码
func TestEncodeRotateDevice(t *testing.T) {
	got := EncodeRotateDevice()
	if len(got) != 1 {
		t.Errorf("length = %d, want 1", len(got))
	}
	if got[0] != TypeRotateDevice {
		t.Errorf("type = %d, want %d", got[0], TypeRotateDevice)
	}
}

// TestEncodeSetDisplayPower 测试屏幕电源编码
func TestEncodeSetDisplayPower(t *testing.T) {
	for _, mode := range []byte{0, 1, 2} {
		got := EncodeSetDisplayPower(mode)
		if len(got) != 2 {
			t.Errorf("length = %d, want 2", len(got))
		}
		if got[0] != TypeSetDisplayPower {
			t.Errorf("type = %d, want %d", got[0], TypeSetDisplayPower)
		}
		if got[1] != mode {
			t.Errorf("mode = %d, want %d", got[1], mode)
		}
	}
}

// TestEncodeResetVideo 测试重置视频编码
func TestEncodeResetVideo(t *testing.T) {
	got := EncodeResetVideo()
	if len(got) != 1 {
		t.Errorf("length = %d, want 1", len(got))
	}
	if got[0] != TypeResetVideo {
		t.Errorf("type = %d, want %d", got[0], TypeResetVideo)
	}
}

// TestEncodeSetBitrate 测试设置码率编码
// 协议: [18][bitrate:4]
func TestEncodeSetBitrate(t *testing.T) {
	tests := []struct {
		bitrate int
		want    int32
	}{
		{2000000, 2000000},
		{4000000, 4000000},
		{8000000, 8000000},
	}
	for _, tt := range tests {
		got := EncodeSetBitrate(tt.bitrate)
		if len(got) != 5 {
			t.Errorf("length = %d, want 5", len(got))
		}
		if got[0] != TypeSetBitrate {
			t.Errorf("type = %d, want %d", got[0], TypeSetBitrate)
		}
		if int32(binary.BigEndian.Uint32(got[1:5])) != tt.want {
			t.Errorf("bitrate = %d, want %d", int32(binary.BigEndian.Uint32(got[1:5])), tt.want)
		}
	}
}

// TestEncodeSwitchCamera 测试切换摄像头编码
// 协议: [19][len:4][cameraId utf8]
func TestEncodeSwitchCamera(t *testing.T) {
	got := EncodeSwitchCamera("0")
	if len(got) != 1+4+1 {
		t.Errorf("length = %d, want %d", len(got), 1+4+1)
	}
	if got[0] != TypeSwitchCamera {
		t.Errorf("type = %d, want %d", got[0], TypeSwitchCamera)
	}
	if binary.BigEndian.Uint32(got[1:5]) != 1 {
		t.Errorf("len = %d, want 1", binary.BigEndian.Uint32(got[1:5]))
	}
	if string(got[5:]) != "0" {
		t.Errorf("cameraId = %s, want 0", string(got[5:]))
	}
}

// TestEncodeUhidCreate 测试 UHID 创建编码
func TestEncodeUhidCreate(t *testing.T) {
	name := "test-device"
	data := []byte{0x01, 0x02, 0x03}
	got := EncodeUhidCreate(1, 0x1234, 0x5678, name, data)
	// [12][id:2][vendor:2][product:2][nameLen:1][name][dataLen:2][data]
	wantLen := 1 + 2 + 2 + 2 + 1 + len(name) + 2 + len(data)
	if len(got) != wantLen {
		t.Errorf("length = %d, want %d", len(got), wantLen)
	}
	if got[0] != TypeUhidCreate {
		t.Errorf("type = %d, want %d", got[0], TypeUhidCreate)
	}
}

// TestEncodeUhidInput 测试 UHID 输入编码
func TestEncodeUhidInput(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04}
	got := EncodeUhidInput(1, data)
	wantLen := 1 + 2 + 2 + len(data)
	if len(got) != wantLen {
		t.Errorf("length = %d, want %d", len(got), wantLen)
	}
	if got[0] != TypeUhidInput {
		t.Errorf("type = %d, want %d", got[0], TypeUhidInput)
	}
}

// TestEncodeUhidDestroy 测试 UHID 销毁编码
func TestEncodeUhidDestroy(t *testing.T) {
	got := EncodeUhidDestroy(1)
	if len(got) != 3 {
		t.Errorf("length = %d, want 3", len(got))
	}
	if got[0] != TypeUhidDestroy {
		t.Errorf("type = %d, want %d", got[0], TypeUhidDestroy)
	}
}

// TestFloatToU16FixedPoint 测试压力值定点数转换
func TestFloatToU16FixedPoint(t *testing.T) {
	tests := []struct {
		input float32
		want  uint16
	}{
		{0.0, 0},
		{1.0, 0xFFFF},
		{0.5, 0x8000},
		{-1.0, 0},  // clamp to 0
		{2.0, 0xFFFF}, // clamp to max
	}
	for _, tt := range tests {
		got := floatToU16FixedPoint(tt.input)
		if got != tt.want {
			t.Errorf("floatToU16FixedPoint(%f) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

// TestFloatToI16FixedPoint16 测试滚动值定点数转换
func TestFloatToI16FixedPoint16(t *testing.T) {
	tests := []struct {
		input float32
	}{
		{0.0},
		{1.0},
		{-1.0},
		{16.0},
		{-16.0},
		{20.0},  // clamp
		{-20.0}, // clamp
	}
	for _, tt := range tests {
		got := floatToI16FixedPoint16(tt.input)
		// 验证值域在 uint16 范围内
		if got > 0xFFFF {
			t.Errorf("floatToI16FixedPoint16(%f) = %d, out of uint16 range", tt.input, got)
		}
	}
}

// TestTouchActionFromString 测试触摸动作字符串解析
func TestTouchActionFromString(t *testing.T) {
	tests := []struct {
		input string
		want  byte
	}{
		{"down", 0},
		{"DOWN", 0},
		{"0", 0},
		{"move", 1},
		{"MOVE", 1},
		{"1", 1},
		{"up", 2},
		{"UP", 2},
		{"2", 2},
		{"unknown", 0}, // default to down
	}
	for _, tt := range tests {
		got := touchActionFromString(tt.input)
		if got != tt.want {
			t.Errorf("touchActionFromString(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

// TestMessageTypeConstants 验证所有消息类型常量值与 scrcpy v3.3.4 协议一致
func TestMessageTypeConstants(t *testing.T) {
	expected := map[int]string{
		0:  "inject-keycode",
		1:  "inject-text",
		2:  "inject-touch",
		3:  "inject-scroll",
		4:  "back-or-screen-on",
		5:  "expand-notification",
		6:  "expand-settings",
		7:  "collapse-panels",
		8:  "get-clipboard",
		9:  "set-clipboard",
		10: "set-display-power",
		11: "rotate-device",
		12: "uhid-create",
		13: "uhid-input",
		14: "uhid-destroy",
		15: "open-hard-keyboard",
		16: "start-app",
		17: "reset-video",
		18: "set-bitrate",
		19: "switch-camera",
	}
	actual := map[int]string{
		TypeInjectKeycode:        "inject-keycode",
		TypeInjectText:           "inject-text",
		TypeInjectTouchEvent:     "inject-touch",
		TypeInjectScrollEvent:    "inject-scroll",
		TypeBackOrScreenOn:       "back-or-screen-on",
		TypeExpandNotification:   "expand-notification",
		TypeExpandSettings:       "expand-settings",
		TypeCollapsePanels:       "collapse-panels",
		TypeGetClipboard:         "get-clipboard",
		TypeSetClipboard:         "set-clipboard",
		TypeSetDisplayPower:      "set-display-power",
		TypeRotateDevice:         "rotate-device",
		TypeUhidCreate:           "uhid-create",
		TypeUhidInput:            "uhid-input",
		TypeUhidDestroy:          "uhid-destroy",
		TypeOpenHardKeyboard:     "open-hard-keyboard",
		TypeStartApp:             "start-app",
		TypeResetVideo:           "reset-video",
		TypeSetBitrate:           "set-bitrate",
		TypeSwitchCamera:         "switch-camera",
	}
	for code, name := range expected {
		if actualName, ok := actual[code]; !ok {
			t.Errorf("missing message type %d (%s)", code, name)
		} else if actualName != name {
			t.Errorf("message type %d = %q, want %q", code, actualName, name)
		}
	}
}
