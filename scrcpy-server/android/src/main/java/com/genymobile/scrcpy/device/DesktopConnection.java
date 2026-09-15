package com.genymobile.scrcpy.device;

import com.genymobile.scrcpy.Options;
import com.genymobile.scrcpy.control.ControlChannel;
import com.genymobile.scrcpy.util.IO;
import com.genymobile.scrcpy.util.StringUtils;

import android.net.LocalSocket;
import android.net.LocalSocketAddress;

import java.io.Closeable;
import java.io.FileDescriptor;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.Socket;
import java.nio.charset.StandardCharsets;

/**
 * 移植自本项目（cloudphone-v0.3.6 / ScrcpyOverWebRTC）libsys_core fork 的 DesktopConnection。
 *
 * 与上游 scrcpy v3.3.4 的核心差异（证据：反编译的 com.android.helper.device.DesktopConnection）：
 *  1. 四通道：video / audio / control / touch（上游仅 video / audio / control）。
 *     touch 通道独立于 control，对应 Go Agent 侧的 uds_sys_t_ 通道；
 *  2. 通道以 SocketWrapper 抽象封装，支持两种底层：
 *     - LocalSocketWrapper：Android LocalSocket 抽象名（"scrcpy" / "scrcpy_%08x"），上游语义；
 *     - NetSocketWrapper：TCP 回环连接 127.0.0.1:port（fork 新增，反编译证据 connectNet(int)）；
 *  3. 入口签名 open(Options)（fork 从 Options 读取 video/audio/control 开关与端口；
 *     上游为 open(scid, tunnelForward, video, audio, control, sendDummyByte)）。
 *
 * 注意：fork 的 open(Options) 方法反编译失败（CFR ConfusedCFRException），
 * 本文件中的 open(Options) 为基于构造器/connectLocal/connectNet 证据的重建实现，
 * 端口与通道的对应关系需按实际 Agent 部署配置核对（见 README-PORT.md）。
 */
public final class DesktopConnection implements Closeable {

    private static final int DEVICE_NAME_FIELD_LENGTH = 64;
    private static final String SOCKET_NAME_PREFIX = "scrcpy";
    /** UDS 多通道：每个通道独立的抽象 socket 名（三通道物理隔离） */
    private static final String SOCKET_VIDEO = "scrcpy_video";
    private static final String SOCKET_AUDIO = "scrcpy_audio";
    private static final String SOCKET_TOUCH = "scrcpy_touch";
    private static final String SOCKET_CONTROL = "scrcpy_control";

    /** 通道底层抽象：LocalSocket 或 TCP Socket 统一接口（fork 差异点）。 */
    private interface SocketWrapper extends Closeable {
        FileDescriptor getFileDescriptor() throws IOException;

        InputStream getInputStream() throws IOException;

        OutputStream getOutputStream() throws IOException;

        void shutdownInput() throws IOException;

        void shutdownOutput() throws IOException;
    }

    private static final class LocalSocketWrapper implements SocketWrapper {
        private final LocalSocket socket;

        LocalSocketWrapper(LocalSocket socket) {
            this.socket = socket;
        }

        @Override
        public FileDescriptor getFileDescriptor() {
            return socket.getFileDescriptor();
        }

        @Override
        public InputStream getInputStream() throws IOException {
            return socket.getInputStream();
        }

        @Override
        public OutputStream getOutputStream() throws IOException {
            return socket.getOutputStream();
        }

        @Override
        public void shutdownInput() throws IOException {
            socket.shutdownInput();
        }

        @Override
        public void shutdownOutput() throws IOException {
            socket.shutdownOutput();
        }

        @Override
        public void close() throws IOException {
            socket.close();
        }
    }

    private static final class NetSocketWrapper implements SocketWrapper {
        private final Socket socket;

        NetSocketWrapper(Socket socket) {
            this.socket = socket;
        }

        @Override
        public FileDescriptor getFileDescriptor() throws IOException {
            // TCP Socket 没有可直接投递的 FileDescriptor，video/audio 走 Socket 流而非 fd
            return null;
        }

        @Override
        public InputStream getInputStream() throws IOException {
            return socket.getInputStream();
        }

        @Override
        public OutputStream getOutputStream() throws IOException {
            return socket.getOutputStream();
        }

        @Override
        public void shutdownInput() throws IOException {
            socket.shutdownInput();
        }

        @Override
        public void shutdownOutput() throws IOException {
            socket.shutdownOutput();
        }

        @Override
        public void close() throws IOException {
            socket.close();
        }
    }

    private final SocketWrapper videoSocket;
    private final FileDescriptor videoFd;

    private final SocketWrapper audioSocket;
    private final FileDescriptor audioFd;

    private final SocketWrapper controlSocket;
    private final ControlChannel controlChannel;

    private final SocketWrapper touchSocket;
    private final ControlChannel touchChannel;

    private DesktopConnection(SocketWrapper videoSocket, SocketWrapper audioSocket, SocketWrapper controlSocket,
                              SocketWrapper touchSocket) throws IOException {
        this.videoSocket = videoSocket;
        this.audioSocket = audioSocket;
        this.controlSocket = controlSocket;
        this.touchSocket = touchSocket;

        videoFd = videoSocket != null ? videoSocket.getFileDescriptor() : null;
        audioFd = audioSocket != null ? audioSocket.getFileDescriptor() : null;
        controlChannel = controlSocket != null ? new ControlChannel(controlSocket.getInputStream(), controlSocket.getOutputStream()) : null;
        touchChannel = touchSocket != null ? new ControlChannel(touchSocket.getInputStream(), touchSocket.getOutputStream()) : null;
    }

    private static SocketWrapper connectLocal(String abstractName) throws IOException {
        LocalSocket localSocket = new LocalSocket();
        localSocket.connect(new LocalSocketAddress(abstractName));
        return new LocalSocketWrapper(localSocket);
    }

    private static SocketWrapper connectNet(int port) throws IOException {
        Socket socket = new Socket("127.0.0.1", port);
        return new NetSocketWrapper(socket);
    }

    private static String getSocketName(int scid) {
        if (scid == -1) {
            return SOCKET_NAME_PREFIX;
        }
        return SOCKET_NAME_PREFIX + String.format("_%08x", scid);
    }

    /**
     * fork 差异：open(Options)。由 Options 决定通道开关与连接方式。
     * 重建说明：fork 反编译显示 Options 含 port 字段，且存在 connectNet(int)（TCP 回环），
     * 因此采用「优先 TCP 回环端口、LocalSocket 抽象名兜底」的策略。
     *
     * @param useTcp  true 用 TCP 回环（options.getPort() 起依次分配 v/a/t/c 四端口）；
     *                false 用 LocalSocket 抽象名（仅 v/a/c 三通道，兼容上游 tunnelForward 语义）。
     */
    public static DesktopConnection open(Options options, boolean useTcp) throws IOException {
        boolean video = options.getVideo();
        boolean audio = options.getAudio();
        boolean control = options.getControl();
        boolean touch = true; // fork 的独立触摸通道始终启用（无上游对应开关，按证据默认开启）

        SocketWrapper videoSocket = null;
        SocketWrapper audioSocket = null;
        SocketWrapper controlSocket = null;
        SocketWrapper touchSocket = null;

        try {
            if (useTcp) {
                int basePort = options.getPort();
                if (video) {
                    videoSocket = connectNet(basePort);
                }
                if (audio) {
                    audioSocket = connectNet(basePort + 1);
                }
                if (touch) {
                    touchSocket = connectNet(basePort + 2);
                }
                if (control) {
                    controlSocket = connectNet(basePort + 3);
                }
            } else {
                // UDS 多通道：每个通道使用独立的抽象 socket 名（三通道物理隔离）
                if (video) {
                    videoSocket = connectLocal(SOCKET_VIDEO);
                }
                if (audio) {
                    audioSocket = connectLocal(SOCKET_AUDIO);
                }
                if (touch) {
                    touchSocket = connectLocal(SOCKET_TOUCH);
                }
                if (control) {
                    controlSocket = connectLocal(SOCKET_CONTROL);
                }
            }
        } catch (IOException | RuntimeException e) {
            closeQuietly(videoSocket);
            closeQuietly(audioSocket);
            closeQuietly(controlSocket);
            closeQuietly(touchSocket);
            throw e;
        }

        return new DesktopConnection(videoSocket, audioSocket, controlSocket, touchSocket);
    }

    private static void closeQuietly(SocketWrapper socket) {
        if (socket != null) {
            try {
                socket.close();
            } catch (IOException e) {
                // ignore
            }
        }
    }

    private SocketWrapper getFirstSocket() {
        if (videoSocket != null) {
            return videoSocket;
        }
        if (audioSocket != null) {
            return audioSocket;
        }
        if (touchSocket != null) {
            return touchSocket;
        }
        return controlSocket;
    }

    public void shutdown() throws IOException {
        shutdownQuietly(videoSocket);
        shutdownQuietly(audioSocket);
        shutdownQuietly(touchSocket);
        shutdownQuietly(controlSocket);
    }

    private static void shutdownQuietly(SocketWrapper socket) throws IOException {
        if (socket != null) {
            socket.shutdownInput();
            socket.shutdownOutput();
        }
    }

    @Override
    public void close() throws IOException {
        closeQuietly(videoSocket);
        closeQuietly(audioSocket);
        closeQuietly(touchSocket);
        closeQuietly(controlSocket);
    }

    public void sendDeviceMeta(String deviceName) throws IOException {
        byte[] buffer = new byte[DEVICE_NAME_FIELD_LENGTH];
        byte[] deviceNameBytes = deviceName.getBytes(StandardCharsets.UTF_8);
        int len = StringUtils.getUtf8TruncationIndex(deviceNameBytes, DEVICE_NAME_FIELD_LENGTH - 1);
        System.arraycopy(deviceNameBytes, 0, buffer, 0, len);

        SocketWrapper first = getFirstSocket();
        if (first instanceof NetSocketWrapper) {
            // TCP 模式无 fd，直接写流
            OutputStream os = first.getOutputStream();
            os.write(buffer, 0, buffer.length);
            os.flush();
        } else {
            IO.writeFully(first.getFileDescriptor(), buffer, 0, buffer.length);
        }
    }

    public FileDescriptor getVideoFd() {
        return videoFd;
    }

    public FileDescriptor getAudioFd() {
        return audioFd;
    }

    /** 本项目移植新增：TCP 回环模式下 Streamer 直接写 OutputStream（videoFd 为 null）。 */
    public OutputStream getVideoOutputStream() throws IOException {
        return videoSocket != null ? videoSocket.getOutputStream() : null;
    }

    /** 本项目移植新增：TCP 回环模式下 Streamer 直接写 OutputStream（audioFd 为 null）。 */
    public OutputStream getAudioOutputStream() throws IOException {
        return audioSocket != null ? audioSocket.getOutputStream() : null;
    }

    public ControlChannel getControlChannel() {
        return controlChannel;
    }

    public ControlChannel getTouchChannel() {
        return touchChannel;
    }
}
