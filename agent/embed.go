package main

import (
	"embed"
	"os"
	"path/filepath"
)

//go:embed libsys_core.so
var scrcpyServerJar embed.FS

// ensureScrcpyServerJar 确保 libsys_core.so 文件存在于指定路径
// 如果文件不存在，从嵌入的资源中释放
func ensureScrcpyServerJar(targetPath string) error {
	// 检查文件是否已存在
	if _, err := os.Stat(targetPath); err == nil {
		// 文件已存在，检查大小是否正确
		info, err := os.Stat(targetPath)
		if err == nil && info.Size() > 0 {
			logf("[Embed] libsys_core.so already exists at %s (%d bytes)", targetPath, info.Size())
			return nil
		}
	}

	// 确保目标目录存在
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// 从嵌入的资源中读取 jar 文件
	data, err := scrcpyServerJar.ReadFile("libsys_core.so")
	if err != nil {
		return err
	}

	// 写入到目标路径
	if err := os.WriteFile(targetPath, data, 0755); err != nil {
		return err
	}

	logf("[Embed] libsys_core.so extracted to %s (%d bytes)", targetPath, len(data))
	return nil
}
