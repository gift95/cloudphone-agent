package com.genymobile.scrcpy.control;

import com.genymobile.scrcpy.device.Position;
import com.genymobile.scrcpy.util.Ln;

import java.io.DataInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;

/**
 * TouchChannel - 独立触摸事件通道
 *
 * 移植自本项目 libsys_core fork 的触摸分流设计：
 *  - 上游 scrcpy 将所有控制消息（触摸/键盘/剪贴板/命令）走单一 control 通道；
 *  - fork 将触摸事件拆分为独立 touch 通道，控制命令仍走 control 通道；
 *  - 目的：降低触摸事件延迟，避免控制命令（如 set_clipboard）阻塞触摸输入。
 *
 * 触摸事件二进制格式（31 字节，不含 type 字节，与 control 通道的 inject_touch 消息 payload 一致）：
 *   [action:1][pointerId:8][x:4][y:4][screenW:2][screenH:2][pressure:2][actionButton:4][buttons:4]
 *
 * Go Agent 侧对应 uds_sys_touch_ 抽象 socket，将前端触控事件直接写入此通道。
 */
public final class TouchChannel {

    public static final class TouchEvent {
        public final int action;
        public final long pointerId;
        public final Position position;
        public final float pressure;
        public final int actionButton;
        public final int buttons;

        public TouchEvent(int action, long pointerId, Position position, float pressure, int actionButton, int buttons) {
            this.action = action;
            this.pointerId = pointerId;
            this.position = position;
            this.pressure = pressure;
            this.actionButton = actionButton;
            this.buttons = buttons;
        }
    }

    private final DataInputStream inputStream;
    private final OutputStream outputStream;
    private volatile boolean running;

    public TouchChannel(InputStream inputStream, OutputStream outputStream) {
        this.inputStream = new DataInputStream(inputStream);
        this.outputStream = outputStream;
        this.running = true;
    }

    /**
     * 读取一个触摸事件（阻塞）
     * @return TouchEvent，通道关闭时返回 null
     */
    public TouchEvent readTouchEvent() throws IOException {
        if (!running) {
            return null;
        }
        try {
            int action = inputStream.readUnsignedByte();
            long pointerId = inputStream.readLong();
            int x = inputStream.readInt();
            int y = inputStream.readInt();
            int screenWidth = inputStream.readUnsignedShort();
            int screenHeight = inputStream.readUnsignedShort();
            int pressureRaw = inputStream.readUnsignedShort();
            int actionButton = inputStream.readInt();
            int buttons = inputStream.readInt();

            // pressure 是 u16 定点数（0.16），转换为 float
            float pressure = pressureRaw / 65535.0f;

            Position position = new Position(x, y, screenWidth, screenHeight);
            return new TouchEvent(action, pointerId, position, pressure, actionButton, buttons);
        } catch (IOException e) {
            if (running) {
                Ln.w("TouchChannel read error: " + e.getMessage());
            }
            return null;
        }
    }

    /**
     * 启动触摸事件读取循环，在独立线程中调用 handler 处理每个触摸事件
     */
    public void startLoop(TouchEventHandler handler) {
        Thread thread = new Thread(() -> {
            Ln.i("TouchChannel reader loop started");
            while (running) {
                try {
                    TouchEvent event = readTouchEvent();
                    if (event == null) {
                        break;
                    }
                    handler.onTouchEvent(event);
                } catch (IOException e) {
                    if (running) {
                        Ln.e("TouchChannel loop error", e);
                    }
                    break;
                }
            }
            Ln.i("TouchChannel reader loop stopped");
        }, "scrcpy-touch-channel");
        thread.setDaemon(true);
        thread.start();
    }

    public interface TouchEventHandler {
        void onTouchEvent(TouchEvent event);
    }

    public void close() throws IOException {
        running = false;
        if (outputStream != null) {
            outputStream.close();
        }
        if (inputStream != null) {
            inputStream.close();
        }
    }
}
