package http

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	runtimeFileTraceLinePattern = regexp.MustCompile(`^\s*(\d{2}:\d{2}:\d{2}\.\d+)\s+(\S+)\s+(.*)$`)
	runtimeFileTraceBytePattern = regexp.MustCompile(`(?:^|\s)B=(0x[0-9a-fA-F]+|\d+)(?:\s|$)`)
	runtimeFileTraceFDPattern   = regexp.MustCompile(`(?:^|\s)F=(\d+)(?:\s|$)`)
	runtimeFileTraceTailPattern = regexp.MustCompile(`\s+(\d+\.\d+)\s+(?:[A-Z*]+\s+)?(\S+)\s*$`)
)

type runtimeFileTraceOpenFile struct {
	Path string
	Type string
}

func runtimeFileTraceTargetPIDs(targets []runtimeFileTraceTarget) map[string][]int {
	result := make(map[string][]int)
	for _, target := range targets {
		name := strings.ToLower(strings.TrimSpace(target.Name))
		if name == "" || target.PID <= 0 {
			continue
		}
		result[name] = append(result[name], target.PID)
	}
	return result
}

func resolveRuntimeFileTraceEvent(event runtimeFileTraceEvent, processPIDs map[string][]int, openFiles map[int]map[int]runtimeFileTraceOpenFile) runtimeFileTraceEvent {
	name := runtimeFileTraceProcessName(event.Process)
	candidates := processPIDs[name]
	if len(candidates) == 1 {
		event.PID = candidates[0]
	}
	if !event.hasFD || strings.TrimSpace(event.Path) != "" {
		return event
	}
	type match struct {
		pid  int
		info runtimeFileTraceOpenFile
	}
	matches := make([]match, 0, len(candidates))
	for _, pid := range candidates {
		if info, exists := openFiles[pid][event.FD]; exists {
			matches = append(matches, match{pid: pid, info: info})
		}
	}
	if len(matches) == 0 {
		return event
	}
	selected := matches[0]
	for _, item := range matches[1:] {
		if item.info != selected.info {
			return event
		}
	}
	event.Path = selected.info.Path
	event.fileType = selected.info.Type
	event.PathSource = "fd_snapshot"
	if len(matches) == 1 {
		event.PID = selected.pid
	}
	return event
}

func runtimeFileTraceEventIsFile(event runtimeFileTraceEvent) bool {
	operation := strings.ToLower(strings.TrimSpace(event.Operation))
	for _, ignored := range []string{"kevent", "kqueue", "select", "poll", "workq"} {
		if strings.Contains(operation, ignored) {
			return false
		}
	}
	path := strings.TrimSpace(event.Path)
	if path == "" {
		// fs_usage also reports terminal, pipe and kernel synchronization I/O.
		// Without either a path from fs_usage or an FD snapshot it is not a
		// verifiable file operation and must not affect rankings/auto-stop.
		return false
	}
	lowerPath := strings.ToLower(path)
	if lowerPath == "/dev/tty" || strings.HasPrefix(lowerPath, "/dev/tty") ||
		lowerPath == "/dev/null" || strings.HasPrefix(lowerPath, "pipe:") ||
		strings.HasPrefix(lowerPath, "socket:") || strings.HasPrefix(lowerPath, "->") {
		return false
	}
	if lowerPath == "/proc" || strings.HasPrefix(lowerPath, "/proc/") {
		return false
	}
	if event.fileType != "" && event.fileType != "REG" && event.fileType != "DIR" {
		return false
	}
	return true
}

func parseRuntimeFileTraceLine(line string, now time.Time) (runtimeFileTraceEvent, bool) {
	line = strings.TrimSpace(strings.ReplaceAll(line, "\x00", ""))
	if line == "" {
		return runtimeFileTraceEvent{}, false
	}
	if !utf8.ValidString(line) {
		line = strings.ToValidUTF8(line, "�")
	}
	matches := runtimeFileTraceLinePattern.FindStringSubmatch(line)
	if len(matches) != 4 {
		return runtimeFileTraceEvent{}, false
	}
	operation := strings.TrimSpace(matches[2])
	if operation == "" || strings.EqualFold(operation, "CALL") {
		return runtimeFileTraceEvent{}, false
	}

	eventAt := runtimeFileTraceTimestamp(matches[1], now)
	event := runtimeFileTraceEvent{
		At:        eventAt.Format(time.RFC3339Nano),
		Kind:      runtimeFileTraceKind(operation),
		Operation: operation,
		Raw:       truncateRuntimeFileTraceText(line, 2_000),
	}
	if byteMatch := runtimeFileTraceBytePattern.FindStringSubmatch(line); len(byteMatch) == 2 {
		if parsed, err := strconv.ParseUint(byteMatch[1], 0, 64); err == nil {
			event.Bytes = parsed
		}
	}
	if fdMatch := runtimeFileTraceFDPattern.FindStringSubmatch(line); len(fdMatch) == 2 {
		if parsed, err := strconv.Atoi(fdMatch[1]); err == nil && parsed >= 0 {
			event.FD = parsed
			event.hasFD = true
		}
	}

	tailStart := len(line)
	if indices := runtimeFileTraceTailPattern.FindStringSubmatchIndex(line); len(indices) >= 6 {
		tailStart = indices[0]
		if seconds, err := strconv.ParseFloat(line[indices[2]:indices[3]], 64); err == nil {
			event.DurationMS = roundedMilliseconds(time.Duration(seconds * float64(time.Second)))
		}
		event.Process = truncateRuntimeFileTraceText(line[indices[4]:indices[5]], 180)
		event.ThreadID = runtimeFileTraceProcessThreadID(event.Process)
	}
	if pathStart := strings.Index(line[:tailStart], "/"); pathStart >= 0 {
		event.Path = truncateRuntimeFileTraceText(strings.TrimSpace(line[pathStart:tailStart]), 1_200)
		event.PathSource = "fs_usage"
	}
	return event, true
}

func runtimeFileTraceTimestamp(clock string, now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now()
	}
	value := now.Format("2006-01-02") + " " + clock
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", value, now.Location())
	if err != nil {
		return now
	}
	if parsed.After(now.Add(time.Hour)) {
		parsed = parsed.AddDate(0, 0, -1)
	}
	return parsed
}

func runtimeFileTraceKind(operation string) string {
	lower := strings.ToLower(operation)
	for _, marker := range []string{"wr", "write", "pwrite", "pageout", "fsync", "rename", "unlink", "mkdir", "rmdir", "create", "truncate", "setattr", "delete", "exchange", "clone", "link"} {
		if strings.Contains(lower, marker) {
			return "write"
		}
	}
	for _, marker := range []string{"rd", "read", "pread", "pagein", "readdir"} {
		if strings.Contains(lower, marker) {
			return "read"
		}
	}
	return "metadata"
}

func runtimeFileTraceProcessThreadID(process string) int {
	index := strings.LastIndex(process, ".")
	if index < 0 || index+1 >= len(process) {
		return 0
	}
	pid, err := strconv.Atoi(process[index+1:])
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func runtimeFileTraceProcessName(process string) string {
	process = strings.TrimSpace(process)
	index := strings.LastIndex(process, ".")
	if index > 0 && index+1 < len(process) {
		if _, err := strconv.Atoi(process[index+1:]); err == nil {
			process = process[:index]
		}
	}
	return strings.ToLower(strings.TrimSpace(process))
}

func truncateRuntimeFileTraceText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return strings.TrimSpace(value[:limit]) + "…"
}

func parseRuntimeFileTraceOpenFiles(raw string) map[int]map[int]runtimeFileTraceOpenFile {
	result := make(map[int]map[int]runtimeFileTraceOpenFile)
	pid := 0
	fd := -1
	fileType := ""
	commit := func(path string) {
		path = strings.TrimSpace(path)
		if pid <= 0 || fd < 0 || path == "" {
			return
		}
		if result[pid] == nil {
			result[pid] = make(map[int]runtimeFileTraceOpenFile)
		}
		result[pid][fd] = runtimeFileTraceOpenFile{Path: path, Type: fileType}
	}
	for _, line := range strings.Split(raw, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(strings.TrimSpace(line[1:]))
			fd = -1
			fileType = ""
		case 'f':
			value := strings.TrimSpace(line[1:])
			end := 0
			for end < len(value) && value[end] >= '0' && value[end] <= '9' {
				end++
			}
			fd = -1
			if end > 0 {
				fd, _ = strconv.Atoi(value[:end])
			}
			fileType = ""
		case 't':
			fileType = strings.TrimSpace(line[1:])
		case 'n':
			commit(line[1:])
		}
	}
	return result
}
