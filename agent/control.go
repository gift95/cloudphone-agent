package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"time"
)

// 全局日志时区（由 main.go 设置）
var logTimezone *time.Location


// scrcpy-server 控制协议编码器（与 scrcpy v3.3.4 ControlMessageReader 格式一致）
// 所有整数大端；定点数：u16 满量程 0x10000，i16 满量程 0x8000（scroll 实际范围 ±16）

const (
	TypeInjectKeycode        = 0
	TypeInjectText           = 1
	TypeInjectTouchEvent     = 2
	TypeInjectScrollEvent    = 3
	TypeBackOrScreenOn       = 4
	TypeExpandNotification   = 5
	TypeExpandSettings       = 6
	TypeCollapsePanels       = 7
	TypeGetClipboard         = 8
	TypeSetClipboard         = 9
	TypeSetDisplayPower      = 10
	TypeRotateDevice         = 11
	TypeUhidCreate           = 12
	TypeUhidInput            = 13
	TypeUhidDestroy          = 14
	TypeOpenHardKeyboard     = 15
	TypeStartApp             = 16
	TypeResetVideo           = 17
	TypeSetBitrate           = 18 // BWE 动态调整编码器码率
	TypeSwitchCamera         = 19 // 动态切换摄像头（不重启 scrcpy-server）
)

func putU16(b []byte, v uint16)    { binary.BigEndian.PutUint16(b, v) }
func putU32(b []byte, v uint32)    { binary.BigEndian.PutUint32(b, v) }
func putI32(b []byte, v int32)     { binary.BigEndian.PutUint32(b, uint32(v)) }
func putU64(b []byte, v uint64)    { binary.BigEndian.PutUint64(b, v) }

// floatToU16FixedPoint 压力/滚轮：满量程 0x10000
func floatToU16FixedPoint(f float32) uint16 {
	if f < 0 {
		return 0
	}
	if f >= 1 {
		return 0xFFFF
	}
	return uint16(f * 0x10000)
}

// floatToI16FixedPoint16 scroll 编码：i16 定点 *16（值域 [-16,16]）
func floatToI16FixedPoint16(f float32) uint16 {
	if f < -16 {
		f = -16
	}
	if f > 16 {
		f = 16
	}
	return uint16(int16(f / 16 * 0x8000))
}

// EncodeInjectKeycode [0][action:1][keycode:4][repeat:4][metaState:4]
func EncodeInjectKeycode(action byte, keycode, repeat, metaState int32) []byte {
	b := make([]byte, 1+1+4+4+4)
	b[0] = TypeInjectKeycode
	b[1] = action
	putI32(b[2:], keycode)
	putI32(b[6:], repeat)
	putI32(b[10:], metaState)
	return b
}

// EncodeInjectText [1][len:4][utf8]
func EncodeInjectText(text string) []byte {
	if len(text) > 300 {
		text = text[:300]
	}
	b := make([]byte, 1+4+len(text))
	b[0] = TypeInjectText
	putU32(b[1:], uint32(len(text)))
	copy(b[5:], text)
	return b
}

// EncodeInjectTouchEvent
// scrcpy 3.3.4 协议（32 字节）：[2][action:1][pointerId:8][x:4 int32][y:4 int32]
//
//	[screenW:2][screenH:2][pressure:2 u16 fixed][actionButton:4][buttons:4]
//
// 注意：x/y 是 int32 像素坐标（parsePosition 用 dis.readInt()），不是 float32
func EncodeInjectTouchEvent(action byte, pointerID uint64, x, y int32, screenW, screenH uint16,
	pressure float32, actionButton, buttons int32) []byte {
	b := make([]byte, 1+1+8+4+4+2+2+2+4+4)
	b[0] = TypeInjectTouchEvent
	b[1] = action
	putU64(b[2:], pointerID)
	putI32(b[10:], x)
	putI32(b[14:], y)
	putU16(b[18:], screenW)
	putU16(b[20:], screenH)
	putU16(b[22:], floatToU16FixedPoint(pressure))
	putI32(b[24:], actionButton)
	putI32(b[28:], buttons)
	return b
}

// EncodeInjectScrollEvent
// scrcpy 3.3.4 协议（21 字节）：[3][x:4 int32][y:4 int32][screenW:2][screenH:2][hScroll:2 i16 fixed][vScroll:2 i16 fixed][buttons:4]
func EncodeInjectScrollEvent(x, y int32, screenW, screenH uint16, hScroll, vScroll float32, buttons int32) []byte {
	b := make([]byte, 1+4+4+2+2+2+2+4)
	b[0] = TypeInjectScrollEvent
	putI32(b[1:], x)
	putI32(b[5:], y)
	putU16(b[9:], screenW)
	putU16(b[11:], screenH)
	putU16(b[13:], floatToI16FixedPoint16(hScroll))
	putU16(b[15:], floatToI16FixedPoint16(vScroll))
	putI32(b[17:], buttons)
	return b
}

// EncodeBackOrScreenOn [4][action:1]
func EncodeBackOrScreenOn(action byte) []byte {
	return []byte{TypeBackOrScreenOn, action}
}

// EncodeSetClipboard [9][sequence:8][paste:1][len:4][utf8]
func EncodeSetClipboard(sequence uint64, text string, paste bool) []byte {
	b := make([]byte, 1+8+1+4+len(text))
	b[0] = TypeSetClipboard
	putU64(b[1:], sequence)
	if paste {
		b[9] = 1
	}
	putU32(b[10:], uint32(len(text)))
	copy(b[14:], text)
	return b
}

// EncodeRotateDevice [11]
func EncodeRotateDevice() []byte { return []byte{TypeRotateDevice} }

// EncodeSetDisplayPower [10][mode:1] 设置屏幕电源模式（0=off, 1=on, 2=normal）
// 用于息屏连接：投屏时关闭物理屏幕背光，防窥省电
func EncodeSetDisplayPower(mode byte) []byte {
	return []byte{TypeSetDisplayPower, mode}
}

// EncodeResetVideo [17]
func EncodeResetVideo() []byte { return []byte{TypeResetVideo} }

// EncodeSetBitrate [18][bitrate:4] BWE 动态调整编码器码率（bps）
func EncodeSetBitrate(bitrate int) []byte {
	b := make([]byte, 1+4)
	b[0] = TypeSetBitrate
	putI32(b[1:], int32(bitrate))
	return b
}

// EncodeSwitchCamera [19][len:4][cameraId utf8] 动态切换摄像头
func EncodeSwitchCamera(cameraId string) []byte {
	b := make([]byte, 1+4+len(cameraId))
	b[0] = TypeSwitchCamera
	putU32(b[1:], uint32(len(cameraId)))
	copy(b[5:], cameraId)
	return b
}

// EncodeUhidCreate [12][id:2][vendor:2][product:2][nameLen:1][name][dataLen:2][data]
func EncodeUhidCreate(id, vendor, product uint16, name string, data []byte) []byte {
	if len(name) > 255 {
		name = name[:255]
	}
	b := make([]byte, 1+2+2+2+1+len(name)+2+len(data))
	b[0] = TypeUhidCreate
	putU16(b[1:], id)
	putU16(b[3:], vendor)
	putU16(b[5:], product)
	b[7] = byte(len(name))
	copy(b[8:], name)
	putU16(b[8+len(name):], uint16(len(data)))
	copy(b[10+len(name):], data)
	return b
}

// EncodeUhidInput [13][id:2][len:2][data]
func EncodeUhidInput(id uint16, data []byte) []byte {
	b := make([]byte, 1+2+2+len(data))
	b[0] = TypeUhidInput
	putU16(b[1:], id)
	putU16(b[3:], uint16(len(data)))
	copy(b[5:], data)
	return b
}

// EncodeUhidDestroy [14][id:2]
func EncodeUhidDestroy(id uint16) []byte {
	b := make([]byte, 1+2)
	b[0] = TypeUhidDestroy
	putU16(b[1:], id)
	return b
}

// 日志工具
func logf(format string, args ...interface{}) {
	now := time.Now()
	if logTimezone != nil {
		now = now.In(logTimezone)
	}
	timestamp := now.Format("2006-01-02 15:04:05.000")
	fmt.Fprintf(os.Stderr, "[%s] [Agent] "+format+"\n", append([]interface{}{timestamp}, args...)...)
}

// SetLogTimezone 设置日志时区
func SetLogTimezone(tz string) {
	if tz == "" {
		logTimezone = nil
		return
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[WARN] invalid timezone %q: %v, using local time\n", tz, err)
		logTimezone = nil
		return
	}
	logTimezone = loc
	fmt.Fprintf(os.Stderr, "[INFO] log timezone set to: %s\n", tz)
}

func debugf(debug bool, format string, args ...interface{}) {
	if debug {
		logf(format, args...)
	}
}

// 从 JSON touch 消息解析 action（前端 sendTouch action 语义：0=DOWN 1=MOVE 2=UP）
func touchActionFromString(s string) byte {
	switch strings.ToLower(s) {
	case "down", "0":
		return 0
	case "move", "1":
		return 1
	case "up", "2":
		return 2
	default:
		return 0
	}
}
