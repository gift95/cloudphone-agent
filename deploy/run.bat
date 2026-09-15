@echo off
setlocal enabledelayedexpansion

if "%~1"=="-h" goto show_help
if "%~1"=="--help" goto show_help

goto init

:show_help
echo Usage: run.bat [adb-serial] [options]
echo Example: run.bat -id my-device -signaling ws://192.168.50.100:8443
echo          run.bat -id my-device -signaling ws://192.168.50.100:8443 -ice-servers "turn:user:pass@192.168.50.100:3478?transport=udp,stun:192.168.50.100:3478"
echo          run.bat -id my-redroid -signaling ws://192.168.50.100:8443 -external-addr 192.168.50.101 -webrtc-port 50000
echo.
echo Options:
echo   -signaling     Signaling server WebSocket URL (required)
echo   -id            Device ID (required)
echo   -jar           Path to scrcpy-server jar/so (default: /data/local/tmp/libsys_core.so)
echo   -bitrate       Target video bitrate in bps
echo   -max-bitrate   Max video bitrate in bps
echo   -max-size      Max video size (0=unlimited)
echo   -max-fps       Max video fps (0=unlimited)
echo   -resolution    Video resolution WxH
echo   -audio         Enable audio (true/false, default: true)
echo   -debug         Enable debug logging
echo   -timezone      Log timezone (e.g., Asia/Shanghai, UTC)
echo   -ice-servers   Comma separated ICE servers (e.g., turn:user:pass@host:port?transport=udp,stun:host:port)
echo   -external-addr External IP for NAT1To1IP
echo   -webrtc-port   WebRTC UDP port (default: 50000)
exit /b 0

:init
set "SERIAL="
set "FIRST_ARG=%~1"

if "%FIRST_ARG%"=="" goto start_process
:: If the first argument does not start with '-', it is considered the adb serial
if not "%FIRST_ARG:~0,1%"=="-" (
    set "SERIAL=-s %FIRST_ARG%"
    shift
)

:start_process
:: Collect all remaining arguments as command-line parameters for the agent
set "AGENT_ARGS="
:args_parse_loop
if "%~1"=="" goto args_parse_done
set "AGENT_ARGS=%AGENT_ARGS% %~1"
shift
goto args_parse_loop
:args_parse_done

:: Detect target device architecture
set "ARCH="
for /f "delims=" %%i in ('adb %SERIAL% shell uname -m 2^>nul') do (
    set "ARCH=%%i"
)

:: If adb command fails or ARCH is empty, report error and exit
if "%ARCH%"=="" (
    echo [ERROR] Failed to detect device architecture. Please check if device is connected via adb.
    exit /b 1
)

:: Remove possible spaces and other trailing characters
set "ARCH=%ARCH: =%"

set "AGENT_BIN=cloudphone-agent-armeabi-v7a"
echo !ARCH! | findstr /i "x86_64 amd64 i686 x86 i386" >nul
if !errorlevel! equ 0 (
    set "AGENT_BIN=cloudphone-agent-amd64"
) else (
    echo !ARCH! | findstr /i "aarch64 arm64" >nul
    if !errorlevel! equ 0 (
        set "AGENT_BIN=cloudphone-agent-arm64"
    )
)

echo === Deploying to device (!ARCH!) ===
echo Pushing !AGENT_BIN!...
adb %SERIAL% push "!AGENT_BIN!" /data/local/tmp/cloudphone-agent-arm64
if !errorlevel! neq 0 (
    echo [ERROR] Failed to push !AGENT_BIN! to device.
    exit /b 1
)

echo Pushing libsys_core.so...
adb %SERIAL% push libsys_core.so /data/local/tmp/libsys_core.so
if !errorlevel! neq 0 (
    echo [ERROR] Failed to push libsys_core.so to device.
    exit /b 1
)

adb %SERIAL% shell chmod +x /data/local/tmp/cloudphone-agent-arm64

echo === Starting Agent ===
:: 1. Force clean old processes (Agent & scrcpy-server)
adb %SERIAL% shell "pkill -f cloudphone-agent || true"
adb %SERIAL% shell "pkill -f com.genymobile.scrcpy || true"

:: 2. Start agent in background and save logs
set "START_CMD=cd /data/local/tmp && CLASSPATH=/data/local/tmp/libsys_core.so nohup /data/local/tmp/cloudphone-agent-arm64 %AGENT_ARGS% > /data/local/tmp/self-agent.log 2>&1 &"
echo Executing: !START_CMD!
adb %SERIAL% shell "sh -c '!START_CMD!'"

:: 3. Verify startup result
timeout /t 2 /nobreak >nul 2>&1
if !errorlevel! neq 0 (
    ping -n 3 127.0.0.1 >nul
)

adb %SERIAL% shell "ps -A | grep cloudphone-agent" >nul 2>&1
if !errorlevel! equ 0 (
    echo === [OK] Agent started successfully in background. ===
    echo Log file: /data/local/tmp/self-agent.log
) else (
    echo === [ERROR] Agent failed to start. Please check logs on device. ===
    adb %SERIAL% shell "tail -30 /data/local/tmp/self-agent.log"
)

endlocal
exit /b 0
