package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// 设备指标采集（Android shell 权限下可读路径）

func getprop(key, def string) string {
	out, err := exec.Command("getprop", key).Output()
	if err != nil {
		return def
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return def
	}
	return s
}

func readProcFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// collectMetrics 输出阶段 2 协议规定的字段：
// cpu, disk_percent, download_speed, memory_percent, temperature, upload_speed
func collectMetrics() map[string]interface{} {
	m := map[string]interface{}{
		"cpu":             readCPUPercent(),
		"disk_percent":    readDiskPercent(),
		"download_speed":  0,
		"memory_percent":  readMemoryPercent(),
		"temperature":     readTemperature(),
		"upload_speed":    0,
	}
	return m
}

func readCPUPercent() float64 {
	// /proc/stat 两次采样
	stat1 := readProcFile("/proc/stat")
	time.Sleep(500 * time.Millisecond)
	stat2 := readProcFile("/proc/stat")
	return cpuDelta(stat1, stat2)
}

func cpuDelta(s1, s2 string) float64 {
	parse := func(s string) (total, idle uint64) {
		fields := strings.Fields(s)
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") {
			return 0, 0
		}
		for _, f := range fields[1:] {
			n, _ := strconv.ParseUint(f, 10, 64)
			total += n
		}
		idle, _ = strconv.ParseUint(fields[4], 10, 64)
		return
	}
	t1, i1 := parse(s1)
	t2, i2 := parse(s2)
	if t2 <= t1 {
		return 0
	}
	return float64((t2 - i2) - (t1 - i1)) / float64(t2-t1) * 100
}

func readMemoryPercent() float64 {
	content := readProcFile("/proc/meminfo")
	total, used := uint64(0), uint64(0)
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		n, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = n
		case "MemAvailable:":
			used = total - n
		}
	}
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

func readDiskPercent() float64 {
	out, err := exec.Command("df", "/data").Output()
	if err != nil {
		return 0
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) < 2 {
		return 0
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 5 {
		return 0
	}
	p := strings.TrimSuffix(fields[4], "%")
	n, _ := strconv.ParseFloat(p, 64)
	return n
}

func readTemperature() float64 {
	// 常见热区路径（shell 可读的优先）
	paths := []string{
		"/sys/class/thermal/thermal_zone0/temp",
		"/sys/class/thermal/thermal_zone1/temp",
	}
	for _, p := range paths {
		s := strings.TrimSpace(readProcFile(p))
		if s == "" {
			continue
		}
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			if n > 1000 {
				n /= 1000 // 部分厂商单位 m°C
			}
			return n
		}
	}
	return 0
}
