package http

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const runtimeFileIOProcessListTimeout = 900 * time.Millisecond

type runtimeFileIOCounter struct {
	readBytes  uint64
	writeBytes uint64
	command    string
	at         time.Time
}

type runtimeFileIOSampler struct {
	at              time.Time
	counters        map[int]runtimeFileIOCounter
	readBytesTotal  uint64
	writeBytesTotal uint64
}

type runtimeFileIOProcessSnapshot struct {
	PID             int     `json:"pid"`
	PPID            int     `json:"ppid"`
	Role            string  `json:"role"`
	Name            string  `json:"name"`
	Command         string  `json:"command"`
	ReadBytesTotal  uint64  `json:"read_bytes_total"`
	WriteBytesTotal uint64  `json:"write_bytes_total"`
	ReadBytesSec    float64 `json:"read_bytes_sec"`
	WriteBytesSec   float64 `json:"write_bytes_sec"`
	Available       bool    `json:"available"`
	Error           string  `json:"error,omitempty"`
}

type runtimeFileIOSnapshot struct {
	Supported             bool                           `json:"supported"`
	SampledAt             string                         `json:"sampled_at"`
	ProcessCount          int                            `json:"process_count"`
	UnavailableCount      int                            `json:"unavailable_count"`
	ReadBytesTotal        uint64                         `json:"read_bytes_total"`
	WriteBytesTotal       uint64                         `json:"write_bytes_total"`
	ActiveReadBytesTotal  uint64                         `json:"active_read_bytes_total"`
	ActiveWriteBytesTotal uint64                         `json:"active_write_bytes_total"`
	ReadBytesSec          float64                        `json:"read_bytes_sec"`
	WriteBytesSec         float64                        `json:"write_bytes_sec"`
	Measurement           string                         `json:"measurement"`
	Error                 string                         `json:"error,omitempty"`
	Processes             []runtimeFileIOProcessSnapshot `json:"processes"`
	DetailTrace           runtimeFileTraceSummary        `json:"detail_trace"`
}

func (s *Service) fileIOSnapshot(now time.Time) runtimeFileIOSnapshot {
	s.runtimeFileIOMu.Lock()
	defer s.runtimeFileIOMu.Unlock()

	result := runtimeFileIOSnapshot{
		SampledAt:   now.Format(time.RFC3339Nano),
		Measurement: "操作系统进程物理磁盘 I/O；累计值保留已观察到的退出进程",
		Processes:   make([]runtimeFileIOProcessSnapshot, 0),
		DetailTrace: s.runtimeFileTraceSummary(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), runtimeFileIOProcessListTimeout)
	defer cancel()
	listed, err := listRuntimeOSProcesses(ctx)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	targets := selectRuntimeFileIOProcesses(listed, os.Getpid())
	result.ProcessCount = len(targets)

	firstSample := s.runtimeFileIO.at.IsZero()
	if s.runtimeFileIO.counters == nil {
		s.runtimeFileIO.counters = make(map[int]runtimeFileIOCounter)
	}

	var readDelta uint64
	var writeDelta uint64
	var readRate float64
	var writeRate float64
	for _, target := range targets {
		entry := runtimeFileIOProcessSnapshot{
			PID:     target.PID,
			PPID:    target.PPID,
			Role:    runtimeFileIORole(target.Command, target.PID == os.Getpid()),
			Name:    runtimeFileIOProcessName(target.Command),
			Command: sanitizeRuntimeProcessCommand(target.Command),
		}
		readBytes, writeBytes, readErr := readRuntimeProcessFileIO(target.PID)
		if readErr != nil {
			entry.Error = readErr.Error()
			result.UnavailableCount++
			result.Processes = append(result.Processes, entry)
			continue
		}

		entry.Available = true
		entry.ReadBytesTotal = readBytes
		entry.WriteBytesTotal = writeBytes
		result.Supported = true
		result.ActiveReadBytesTotal += readBytes
		result.ActiveWriteBytesTotal += writeBytes

		previous, seen := s.runtimeFileIO.counters[target.PID]
		sameProcess := seen && previous.command == target.Command
		currentReadDelta := runtimeFileIODelta(previous.readBytes, readBytes, sameProcess)
		currentWriteDelta := runtimeFileIODelta(previous.writeBytes, writeBytes, sameProcess)
		if firstSample || !sameProcess {
			// The first sample establishes speed baselines while preserving the
			// complete per-process totals already accumulated since startup. A
			// newly discovered long-lived process also starts at zero speed rather
			// than reporting its lifetime bytes as activity in this sample.
		} else if processElapsed := now.Sub(previous.at).Seconds(); processElapsed > 0 && readBytes >= previous.readBytes && writeBytes >= previous.writeBytes {
			entry.ReadBytesSec = roundedRuntimeFileIORate(float64(currentReadDelta) / processElapsed)
			entry.WriteBytesSec = roundedRuntimeFileIORate(float64(currentWriteDelta) / processElapsed)
			readRate += float64(currentReadDelta) / processElapsed
			writeRate += float64(currentWriteDelta) / processElapsed
		}
		readDelta += currentReadDelta
		writeDelta += currentWriteDelta
		s.runtimeFileIO.counters[target.PID] = runtimeFileIOCounter{
			readBytes:  readBytes,
			writeBytes: writeBytes,
			command:    target.Command,
			at:         now,
		}
		result.Processes = append(result.Processes, entry)
	}

	s.runtimeFileIO.readBytesTotal += readDelta
	s.runtimeFileIO.writeBytesTotal += writeDelta
	s.runtimeFileIO.at = now
	result.ReadBytesTotal = s.runtimeFileIO.readBytesTotal
	result.WriteBytesTotal = s.runtimeFileIO.writeBytesTotal
	result.ReadBytesSec = roundedRuntimeFileIORate(readRate)
	result.WriteBytesSec = roundedRuntimeFileIORate(writeRate)
	return result
}

func runtimeFileIODelta(previous, current uint64, sameProcess bool) uint64 {
	if sameProcess && current >= previous {
		return current - previous
	}
	return current
}

func roundedRuntimeFileIORate(value float64) float64 {
	return math.Round(value*100) / 100
}

func selectRuntimeFileIOProcesses(processes []runtimeOSProcess, currentPID int) []runtimeOSProcess {
	selected := make(map[int]struct{})
	byPID := make(map[int]runtimeOSProcess, len(processes)+1)
	for _, process := range processes {
		byPID[process.PID] = process
		if runtimeFileIOMonitorHelper(process.Command) {
			continue
		}
		if process.PID == currentPID || relatedRuntimeProcess(process.Command) {
			selected[process.PID] = struct{}{}
		}
	}
	if _, exists := byPID[currentPID]; !exists && currentPID > 0 {
		current := runtimeOSProcess{
			PID:     currentPID,
			PPID:    os.Getppid(),
			Command: strings.Join(os.Args, " "),
		}
		byPID[currentPID] = current
		selected[currentPID] = struct{}{}
	}

	// Capture helpers and resource trackers whose command line does not repeat
	// the marker of the project process that created them.
	for changed := true; changed; {
		changed = false
		for _, process := range processes {
			if runtimeFileIOMonitorHelper(process.Command) {
				continue
			}
			if _, exists := selected[process.PID]; exists {
				continue
			}
			if _, parentSelected := selected[process.PPID]; parentSelected {
				selected[process.PID] = struct{}{}
				changed = true
			}
		}
	}

	result := make([]runtimeOSProcess, 0, len(selected))
	for pid := range selected {
		if process, exists := byPID[pid]; exists {
			result = append(result, process)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PID == currentPID {
			return true
		}
		if result[j].PID == currentPID {
			return false
		}
		leftRole := runtimeFileIORole(result[i].Command, false)
		rightRole := runtimeFileIORole(result[j].Command, false)
		if leftRole != rightRole {
			return leftRole < rightRole
		}
		return result[i].PID < result[j].PID
	})
	return result
}

func runtimeFileIOMonitorHelper(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	return strings.Contains(lower, "/bin/ps -axo pid=,ppid=,command=") ||
		strings.Contains(lower, "/usr/bin/fs_usage -w -f filesys") ||
		strings.Contains(lower, "chatlog-file-trace-")
}

func runtimeFileIORole(command string, current bool) string {
	if current {
		return "Chatlog 主进程"
	}
	lower := strings.ToLower(command)
	switch {
	case strings.Contains(lower, "mlx_vlm.server"):
		return "OCR 模型服务"
	case strings.Contains(lower, "glmocr.server"):
		return "OCR SDK 服务"
	case strings.Contains(lower, "wechat_image_download_frida.py"):
		return "图片下载 Hook"
	case strings.Contains(lower, "wechat_key_frida.py"):
		return "数据密钥 Hook"
	case strings.Contains(lower, "frida-helper"):
		return "Frida 辅助进程"
	case relatedRuntimeProcess(command):
		return "Chatlog 关联进程"
	default:
		return "衍生子进程"
	}
}

func runtimeFileIOProcessName(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "-"
	}
	return filepath.Base(fields[0])
}

func sanitizeRuntimeProcessCommand(command string) string {
	const maximumLength = 420
	fields := strings.Fields(strings.TrimSpace(command))
	for index := range fields {
		lower := strings.ToLower(fields[index])
		for _, sensitive := range []string{"--data-key", "--api-key", "--token", "--secret", "--password"} {
			if lower == sensitive && index+1 < len(fields) {
				fields[index+1] = "***"
			}
			if strings.HasPrefix(lower, sensitive+"=") {
				fields[index] = fields[index][:len(sensitive)+1] + "***"
			}
		}
	}
	result := strings.Join(fields, " ")
	if len(result) > maximumLength {
		result = result[:maximumLength] + "…"
	}
	return result
}
