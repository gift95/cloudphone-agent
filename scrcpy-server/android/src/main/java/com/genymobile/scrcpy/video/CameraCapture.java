package com.genymobile.scrcpy.video;

import com.genymobile.scrcpy.AndroidVersions;
import com.genymobile.scrcpy.Options;
import com.genymobile.scrcpy.device.ConfigurationException;
import com.genymobile.scrcpy.device.Orientation;
import com.genymobile.scrcpy.device.Size;
import com.genymobile.scrcpy.opengl.AffineOpenGLFilter;
import com.genymobile.scrcpy.opengl.OpenGLFilter;
import com.genymobile.scrcpy.opengl.OpenGLRunner;
import com.genymobile.scrcpy.util.AffineMatrix;
import com.genymobile.scrcpy.util.HandlerExecutor;
import com.genymobile.scrcpy.util.Ln;
import com.genymobile.scrcpy.util.LogUtils;
import com.genymobile.scrcpy.wrappers.ServiceManager;

import android.annotation.SuppressLint;
import android.annotation.TargetApi;
import android.graphics.Rect;
import android.hardware.camera2.CameraAccessException;
import android.hardware.camera2.CameraCaptureSession;
import android.hardware.camera2.CameraCharacteristics;
import android.hardware.camera2.CameraConstrainedHighSpeedCaptureSession;
import android.hardware.camera2.CameraDevice;
import android.hardware.camera2.CameraManager;
import android.hardware.camera2.CaptureFailure;
import android.hardware.camera2.CaptureRequest;
import android.hardware.camera2.params.OutputConfiguration;
import android.hardware.camera2.params.SessionConfiguration;
import android.hardware.camera2.params.StreamConfigurationMap;
import android.media.MediaCodec;
import android.os.Handler;
import android.os.HandlerThread;
import android.util.Range;
import android.view.Surface;

import java.io.IOException;
import java.util.Arrays;
import java.util.List;
import java.util.Optional;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.Executor;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.stream.Stream;

public class CameraCapture extends SurfaceCapture {

    public static final float[] VFLIP_MATRIX = {
            1, 0, 0, 0, // column 1
            0, -1, 0, 0, // column 2
            0, 0, 1, 0, // column 3
            0, 1, 0, 1, // column 4
    };

    private final String explicitCameraId;
    private final CameraFacing cameraFacing;
    private final Size explicitSize;
    private int maxSize;
    private final CameraAspectRatio aspectRatio;
    private final int fps;
    private final boolean highSpeed;
    private final Rect crop;
    private final Orientation captureOrientation;
    private final float angle;

    private String cameraId;
    private Size captureSize;
    private Size videoSize; // after OpenGL transforms

    private AffineMatrix transform;
    private OpenGLRunner glRunner;

    private HandlerThread cameraThread;
    private Handler cameraHandler;
    private CameraDevice cameraDevice;
    private Executor cameraExecutor;
    private Surface encoderSurface; // 保存编码器输入 Surface，用于动态切换摄像头
    private Surface captureSurface; // 保存传递给 CameraCaptureSession 的 Surface

    private final AtomicBoolean disconnected = new AtomicBoolean();

    public CameraCapture(Options options) {
        this.explicitCameraId = options.getCameraId();
        this.cameraFacing = options.getCameraFacing();
        this.explicitSize = options.getCameraSize();
        this.maxSize = options.getMaxSize();
        this.aspectRatio = options.getCameraAspectRatio();
        this.fps = options.getCameraFps();
        this.highSpeed = options.getCameraHighSpeed();
        this.crop = options.getCrop();
        this.captureOrientation = options.getCaptureOrientation();
        assert captureOrientation != null;
        this.angle = options.getAngle();
    }

    @Override
    protected void init() throws ConfigurationException, IOException {
        cameraThread = new HandlerThread("camera");
        cameraThread.start();
        cameraHandler = new Handler(cameraThread.getLooper());
        cameraExecutor = new HandlerExecutor(cameraHandler);

        try {
            cameraId = selectCamera(explicitCameraId, cameraFacing);
            if (cameraId == null) {
                throw new ConfigurationException("No matching camera found");
            }

            Ln.i("Using camera '" + cameraId + "'");
            cameraDevice = openCamera(cameraId);
        } catch (CameraAccessException | InterruptedException e) {
            throw new IOException(e);
        }
    }

    @Override
    public void prepare() throws IOException {
        try {
            captureSize = selectSize(cameraId, explicitSize, maxSize, aspectRatio, highSpeed);
            if (captureSize == null) {
                throw new IOException("Could not select camera size");
            }
        } catch (CameraAccessException e) {
            throw new IOException(e);
        }

        VideoFilter filter = new VideoFilter(captureSize);

        if (crop != null) {
            filter.addCrop(crop, false);
        }

        if (captureOrientation != Orientation.Orient0) {
            filter.addOrientation(captureOrientation);
        }

        filter.addAngle(angle);

        transform = filter.getInverseTransform();
        videoSize = filter.getOutputSize().limit(maxSize).round8();
    }

    private static String selectCamera(String explicitCameraId, CameraFacing cameraFacing) throws CameraAccessException, ConfigurationException {
        CameraManager cameraManager = ServiceManager.getCameraManager();

        String[] cameraIds = cameraManager.getCameraIdList();
        if (explicitCameraId != null) {
            if (!Arrays.asList(cameraIds).contains(explicitCameraId)) {
                Ln.e("Camera with id " + explicitCameraId + " not found\n" + LogUtils.buildCameraListMessage(false));
                throw new ConfigurationException("Camera id not found");
            }
            return explicitCameraId;
        }

        if (cameraFacing == null) {
            // Use the first one
            return cameraIds.length > 0 ? cameraIds[0] : null;
        }

        for (String cameraId : cameraIds) {
            CameraCharacteristics characteristics = cameraManager.getCameraCharacteristics(cameraId);

            int facing = characteristics.get(CameraCharacteristics.LENS_FACING);
            if (cameraFacing.value() == facing) {
                return cameraId;
            }
        }

        // Not found
        return null;
    }

    @TargetApi(AndroidVersions.API_24_ANDROID_7_0)
    private static Size selectSize(String cameraId, Size explicitSize, int maxSize, CameraAspectRatio aspectRatio, boolean highSpeed)
            throws CameraAccessException {
        if (explicitSize != null) {
            return explicitSize;
        }

        CameraManager cameraManager = ServiceManager.getCameraManager();
        CameraCharacteristics characteristics = cameraManager.getCameraCharacteristics(cameraId);

        StreamConfigurationMap configs = characteristics.get(CameraCharacteristics.SCALER_STREAM_CONFIGURATION_MAP);
        android.util.Size[] sizes = highSpeed ? configs.getHighSpeedVideoSizes() : configs.getOutputSizes(MediaCodec.class);
        if (sizes == null) {
            return null;
        }

        Stream<android.util.Size> stream = Arrays.stream(sizes);
        if (maxSize > 0) {
            stream = stream.filter(it -> it.getWidth() <= maxSize && it.getHeight() <= maxSize);
        }

        Float targetAspectRatio = resolveAspectRatio(aspectRatio, characteristics);
        if (targetAspectRatio != null) {
            stream = stream.filter(it -> {
                float ar = ((float) it.getWidth() / it.getHeight());
                float arRatio = ar / targetAspectRatio;
                // Accept if the aspect ratio is the target aspect ratio + or - 10%
                return arRatio >= 0.9f && arRatio <= 1.1f;
            });
        }

        Optional<android.util.Size> selected = stream.max((s1, s2) -> {
            // Greater width is better
            int cmp = Integer.compare(s1.getWidth(), s2.getWidth());
            if (cmp != 0) {
                return cmp;
            }

            if (targetAspectRatio != null) {
                // Closer to the target aspect ratio is better
                float ar1 = ((float) s1.getWidth() / s1.getHeight());
                float arRatio1 = ar1 / targetAspectRatio;
                float distance1 = Math.abs(1 - arRatio1);

                float ar2 = ((float) s2.getWidth() / s2.getHeight());
                float arRatio2 = ar2 / targetAspectRatio;
                float distance2 = Math.abs(1 - arRatio2);

                // Reverse the order because lower distance is better
                cmp = Float.compare(distance2, distance1);
                if (cmp != 0) {
                    return cmp;
                }
            }

            // Greater height is better
            return Integer.compare(s1.getHeight(), s2.getHeight());
        });

        if (selected.isPresent()) {
            android.util.Size size = selected.get();
            return new Size(size.getWidth(), size.getHeight());
        }

        // Not found
        return null;
    }

    private static Float resolveAspectRatio(CameraAspectRatio ratio, CameraCharacteristics characteristics) {
        if (ratio == null) {
            return null;
        }

        if (ratio.isSensor()) {
            Rect activeSize = characteristics.get(CameraCharacteristics.SENSOR_INFO_ACTIVE_ARRAY_SIZE);
            return (float) activeSize.width() / activeSize.height();
        }

        return ratio.getAspectRatio();
    }

    @Override
    public void start(Surface surface) throws IOException {
        this.encoderSurface = surface;
        if (transform != null) {
            assert glRunner == null;
            OpenGLFilter glFilter = new AffineOpenGLFilter(transform);
            // The transform matrix returned by SurfaceTexture is incorrect for camera capture (it often contains an additional unexpected 90°
            // rotation). Use a vertical flip transform matrix instead.
            glRunner = new OpenGLRunner(glFilter, VFLIP_MATRIX);
            surface = glRunner.start(captureSize, videoSize, surface);
        }
        this.captureSurface = surface;

        // 如果正在切换摄像头，等待切换完成（最多5秒）
        if (cameraSwitching) {
            Ln.i("Camera switch in progress, waiting for it to complete...");
            long waitStart = System.currentTimeMillis();
            while (cameraSwitching && (System.currentTimeMillis() - waitStart) < 5000) {
                try {
                    Thread.sleep(100);
                } catch (InterruptedException e) {
                    break;
                }
            }
            Ln.i("Wait finished, cameraSwitching=" + cameraSwitching + ", cameraDevice=" + (cameraDevice != null ? "not null" : "null"));
        }

        // null 检查：避免 NullPointerException
        if (cameraDevice == null) {
            Ln.e("cameraDevice is null, cannot start capture");
            throw new IOException("cameraDevice is null");
        }

        try {
            CameraCaptureSession session = createCaptureSession(cameraDevice, surface);
            CaptureRequest request = createCaptureRequest(surface);
            setRepeatingRequest(session, request);
        } catch (CameraAccessException | InterruptedException e) {
            stop();
            throw new IOException(e);
        }
    }

    @Override
    public void stop() {
        if (glRunner != null) {
            glRunner.stopAndRelease();
            glRunner = null;
        }
    }

    @Override
    public void release() {
        if (cameraDevice != null) {
            cameraDevice.close();
        }
        if (cameraThread != null) {
            cameraThread.quitSafely();
        }
    }

    @Override
    public Size getSize() {
        return videoSize;
    }

    @Override
    public boolean setMaxSize(int maxSize) {
        if (explicitSize != null) {
            return false;
        }

        this.maxSize = maxSize;
        return true;
    }

    @SuppressLint("MissingPermission")
    @TargetApi(AndroidVersions.API_31_ANDROID_12)
    private CameraDevice openCamera(String id) throws CameraAccessException, InterruptedException {
        CompletableFuture<CameraDevice> future = new CompletableFuture<>();
        ServiceManager.getCameraManager().openCamera(id, new CameraDevice.StateCallback() {
            @Override
            public void onOpened(CameraDevice camera) {
                Ln.d("Camera opened successfully");
                future.complete(camera);
            }

            @Override
            public void onDisconnected(CameraDevice camera) {
                Ln.w("Camera disconnected");
                disconnected.set(true);
                invalidate();
            }

            @Override
            public void onError(CameraDevice camera, int error) {
                int cameraAccessExceptionErrorCode;
                switch (error) {
                    case CameraDevice.StateCallback.ERROR_CAMERA_IN_USE:
                        cameraAccessExceptionErrorCode = CameraAccessException.CAMERA_IN_USE;
                        break;
                    case CameraDevice.StateCallback.ERROR_MAX_CAMERAS_IN_USE:
                        cameraAccessExceptionErrorCode = CameraAccessException.MAX_CAMERAS_IN_USE;
                        break;
                    case CameraDevice.StateCallback.ERROR_CAMERA_DISABLED:
                        cameraAccessExceptionErrorCode = CameraAccessException.CAMERA_DISABLED;
                        break;
                    case CameraDevice.StateCallback.ERROR_CAMERA_DEVICE:
                    case CameraDevice.StateCallback.ERROR_CAMERA_SERVICE:
                    default:
                        cameraAccessExceptionErrorCode = CameraAccessException.CAMERA_ERROR;
                        break;
                }
                future.completeExceptionally(new CameraAccessException(cameraAccessExceptionErrorCode));
            }
        }, cameraHandler);

        try {
            return future.get(5000, java.util.concurrent.TimeUnit.MILLISECONDS);
        } catch (ExecutionException e) {
            throw (CameraAccessException) e.getCause();
        } catch (java.util.concurrent.TimeoutException e) {
            throw new CameraAccessException(CameraAccessException.CAMERA_ERROR, "Camera open timeout");
        }
    }

    @TargetApi(AndroidVersions.API_31_ANDROID_12)
    private CameraCaptureSession createCaptureSession(CameraDevice camera, Surface surface) throws CameraAccessException, InterruptedException {
        CompletableFuture<CameraCaptureSession> future = new CompletableFuture<>();
        OutputConfiguration outputConfig = new OutputConfiguration(surface);
        List<OutputConfiguration> outputs = Arrays.asList(outputConfig);

        int sessionType = highSpeed ? SessionConfiguration.SESSION_HIGH_SPEED : SessionConfiguration.SESSION_REGULAR;
        SessionConfiguration sessionConfig = new SessionConfiguration(sessionType, outputs, cameraExecutor, new CameraCaptureSession.StateCallback() {
            @Override
            public void onConfigured(CameraCaptureSession session) {
                future.complete(session);
            }

            @Override
            public void onConfigureFailed(CameraCaptureSession session) {
                future.completeExceptionally(new CameraAccessException(CameraAccessException.CAMERA_ERROR));
            }
        });

        camera.createCaptureSession(sessionConfig);

        try {
            return future.get(5000, java.util.concurrent.TimeUnit.MILLISECONDS);
        } catch (ExecutionException e) {
            throw (CameraAccessException) e.getCause();
        } catch (java.util.concurrent.TimeoutException e) {
            throw new CameraAccessException(CameraAccessException.CAMERA_ERROR, "Camera open timeout");
        }
    }

    private CaptureRequest createCaptureRequest(Surface surface) throws CameraAccessException {
        CaptureRequest.Builder requestBuilder = cameraDevice.createCaptureRequest(CameraDevice.TEMPLATE_RECORD);
        requestBuilder.addTarget(surface);

        if (fps > 0) {
            requestBuilder.set(CaptureRequest.CONTROL_AE_TARGET_FPS_RANGE, new Range<>(fps, fps));
        }

        return requestBuilder.build();
    }

    @TargetApi(AndroidVersions.API_31_ANDROID_12)
    private void setRepeatingRequest(CameraCaptureSession session, CaptureRequest request) throws CameraAccessException, InterruptedException {
        CameraCaptureSession.CaptureCallback callback = new CameraCaptureSession.CaptureCallback() {
            @Override
            public void onCaptureStarted(CameraCaptureSession session, CaptureRequest request, long timestamp, long frameNumber) {
                // Called for each frame captured, do nothing
            }

            @Override
            public void onCaptureFailed(CameraCaptureSession session, CaptureRequest request, CaptureFailure failure) {
                Ln.w("Camera capture failed: frame " + failure.getFrameNumber());
            }
        };

        if (highSpeed) {
            CameraConstrainedHighSpeedCaptureSession highSpeedSession = (CameraConstrainedHighSpeedCaptureSession) session;
            List<CaptureRequest> requests = highSpeedSession.createHighSpeedRequestList(request);
            highSpeedSession.setRepeatingBurst(requests, callback, cameraHandler);
        } else {
            session.setRepeatingRequest(request, callback, cameraHandler);
        }
    }

    @Override
    public boolean isClosed() {
        return disconnected.get();
    }

    @Override
    public void requestInvalidate() {
        // do nothing (the user could not request a reset anyway for now, since there is no controller for camera mirroring)
    }

    private final Object cameraSwitchLock = new Object();
    private volatile boolean cameraSwitching = false;

    /**
     * 动态切换摄像头（不重启 scrcpy-server）
     * 异步执行，带同步锁避免并发调用，失败时回退到旧摄像头
     */
    public void switchCamera(String newCameraId) {
        final String targetCameraId = newCameraId;
        final String oldCameraId = cameraId;

        // 防止并发切换
        if (cameraSwitching) {
            Ln.w("Camera switch already in progress, ignoring request to " + targetCameraId);
            return;
        }

        new Thread(() -> {
            synchronized (cameraSwitchLock) {
                cameraSwitching = true;
                try {
                    Ln.i("Switching camera from '" + oldCameraId + "' to '" + targetCameraId + "'");

                    // 1. 关闭当前的 CameraDevice
                    CameraDevice oldDevice = cameraDevice;
                    cameraDevice = null;
                    if (oldDevice != null) {
                        oldDevice.close();
                    }

                    // 2. 等待旧摄像头完全释放
                    try {
                        Thread.sleep(800);
                    } catch (InterruptedException e) {
                        Ln.w("Wait for camera release interrupted");
                    }

                    // 3. 打开新的 CameraDevice（带重试）
                    CameraDevice newDevice = null;
                    for (int retry = 0; retry < 3; retry++) {
                        try {
                            newDevice = openCamera(targetCameraId);
                            if (newDevice != null) {
                                break;
                            }
                        } catch (Exception e) {
                            Ln.w("Failed to open camera " + targetCameraId + " (retry " + (retry + 1) + "/3): " + e.getMessage());
                            try {
                                Thread.sleep(500);
                            } catch (InterruptedException ie) {
                                break;
                            }
                        }
                    }

                    // 4. 如果打开失败，回退到旧摄像头
                    if (newDevice == null) {
                        Ln.e("Failed to open camera " + targetCameraId + " after 3 retries, falling back to " + oldCameraId);
                        try {
                            newDevice = openCamera(oldCameraId);
                        } catch (Exception e) {
                            Ln.e("Failed to fallback to old camera " + oldCameraId + ": " + e.getMessage());
                        }
                        if (newDevice == null) {
                            Ln.e("Both new and old camera failed, camera capture stopped");
                            return;
                        }
                        cameraId = oldCameraId;
                    } else {
                        cameraId = targetCameraId;
                    }

                    cameraDevice = newDevice;
                    Ln.i("Using camera '" + cameraId + "'");

                    // 5. 使用保存的 captureSurface 创建新的 CaptureSession
                    if (captureSurface != null && captureSurface.isValid()) {
                        try {
                            CameraCaptureSession session = createCaptureSession(cameraDevice, captureSurface);
                            CaptureRequest request = createCaptureRequest(captureSurface);
                            setRepeatingRequest(session, request);
                            Ln.i("Camera switched successfully to " + cameraId);
                        } catch (Exception e) {
                            Ln.e("Failed to create capture session after switch: " + e.getMessage());
                            e.printStackTrace();
                        }
                    } else {
                        Ln.e("captureSurface is null or invalid, cannot switch camera");
                    }
                } catch (Exception e) {
                    Ln.e("Failed to switch camera: " + e.getMessage());
                    e.printStackTrace();
                } finally {
                    cameraSwitching = false;
                }
            }
        }, "CameraSwitchThread").start();
    }
}
