//go:build !darwin

package http

import "fmt"

func runtimeFileTracePlatformSupport() (bool, string) {
	return false, "逐文件内核事件追踪当前仅接入 macOS fs_usage"
}

func startRuntimeFileTracePlatform(_ []int) (runtimeFileTracePlatformSession, error) {
	return nil, fmt.Errorf("逐文件内核事件追踪当前仅接入 macOS fs_usage")
}

func runtimeFileTraceOpenFiles(_ map[int]struct{}) map[int]map[int]runtimeFileTraceOpenFile {
	return nil
}
