package com.genymobile.scrcpy.control;

import com.genymobile.scrcpy.util.IO;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;

/**
 * 移植自本项目 libsys_core fork 的 ControlChannel。
 *
 * fork 差异（反编译证据：com.android.helper.control.ControlChannel）：
 *  上游构造器为 ControlChannel(LocalSocket)，内部取 socket.getInputStream()/getOutputStream()；
 *  fork 直接接受 (InputStream, OutputStream)，配合 DesktopConnection 的四通道注入
 *  （Go Agent 侧 uds_sys_v/a/t/c_ → LocalSocket 或 TCP 回环）。
 *
 * 本移植版保留上游全部读写语义（writeControlMessage / readMessage / sendDeviceMessage），
 * 仅将底层从 LocalSocket 替换为注入的流，实现与 fork 一致。
 */
public final class ControlChannel {

    private final ControlMessageReader reader;
    private final DeviceMessageWriter writer;
    private final OutputStream outputStream;

    public ControlChannel(InputStream inputStream, OutputStream outputStream) {
        reader = new ControlMessageReader(inputStream);
        writer = new DeviceMessageWriter(outputStream);
        this.outputStream = outputStream;
    }

    public ControlMessage readMessage() throws IOException {
        return reader.read();
    }

    public void sendDeviceMessage(DeviceMessage message) throws IOException {
        writer.write(message);
    }

    public void close() throws IOException {
        outputStream.close();
    }
}
