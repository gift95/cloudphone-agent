#!/sbin/sh
SKIPUNZIP=1

# 备份可能已存在的旧配置文件，防止升级时被清空
OLD_CONF="/data/adb/modules/cloudphone-agent/config.conf"
BACKUP_CONF="/data/local/tmp/cloudphone-agent-config.conf.bak"
if [ -f "$OLD_CONF" ]; then
    ui_print "- Backing up existing configuration..."
    cp -af "$OLD_CONF" "$BACKUP_CONF"
fi

# 解压基础框架文件
ui_print "- Extracting Magisk module files..."
unzip -o "$ZIPFILE" module.prop service.sh uninstall.sh libsys_core.so config.conf cloudphone-ctl action.sh -d "$MODPATH" >&2

# 创建可挂载的二进制目录
mkdir -p "$MODPATH/system/bin"

# 自动探测设备架构，选择并提取对应的 Agent 二进制
ui_print "- Target device architecture: $ARCH"
AGENT_BIN=""
case "$ARCH" in
    arm64)
        AGENT_BIN="cloudphone-agent-arm64"
        ;;
    arm)
        AGENT_BIN="cloudphone-agent-armeabi-v7a"
        ;;
    x64)
        AGENT_BIN="cloudphone-agent-amd64"
        ;;
    *)
        ui_print "! Unsupported CPU architecture: $ARCH"
        abort "! Installation aborted. Cloudphone Agent only supports arm64, armeabi-v7a, and x86_64."
        ;;
esac

ui_print "- Extracting $AGENT_BIN..."
unzip -j -o "$ZIPFILE" "binaries/$AGENT_BIN" -d "$MODPATH/system/bin" >&2
mv "$MODPATH/system/bin/$AGENT_BIN" "$MODPATH/system/bin/cloudphone-agent-arm64"

# 移动控制脚本到 system/bin
mv "$MODPATH/cloudphone-ctl" "$MODPATH/system/bin/"
ln -sf cloudphone-ctl "$MODPATH/system/bin/cpctl"

# 配置文件恢复与处理
if [ -f "$BACKUP_CONF" ]; then
    ui_print "- Restoring configuration from backup..."
    cp -af "$BACKUP_CONF" "$MODPATH/config.conf"
    rm -f "$BACKUP_CONF"
else
    ui_print "- No existing configuration found. Using default config.conf."
fi

# 设置文件权限
ui_print "- Setting permissions..."
set_perm "$MODPATH/system/bin/cloudphone-agent-arm64" 0 0 0755
set_perm "$MODPATH/system/bin/cloudphone-ctl" 0 0 0755
set_perm "$MODPATH/service.sh" 0 0 0755
set_perm "$MODPATH/uninstall.sh" 0 0 0755
set_perm "$MODPATH/action.sh" 0 0 0755
set_perm "$MODPATH/libsys_core.so" 0 0 0644
set_perm "$MODPATH/config.conf" 0 0 0600

ui_print "- Installation complete! Reboot device to start Cloudphone Agent."
