//go:build darwin && cgo

package http

/*
#include <libproc.h>
#include <sys/resource.h>
#include <stdint.h>

static int chatlog_proc_disk_io(int pid, uint64_t *read_bytes, uint64_t *write_bytes) {
	struct rusage_info_v2 info = {0};
	int result = proc_pid_rusage(pid, RUSAGE_INFO_V2, (rusage_info_t *)&info);
	if (result != 0) {
		return result;
	}
	*read_bytes = info.ri_diskio_bytesread;
	*write_bytes = info.ri_diskio_byteswritten;
	return 0;
}
*/
import "C"

import "fmt"

func readRuntimeProcessFileIO(pid int) (uint64, uint64, error) {
	var readBytes C.uint64_t
	var writeBytes C.uint64_t
	if result := C.chatlog_proc_disk_io(C.int(pid), &readBytes, &writeBytes); result != 0 {
		return 0, 0, fmt.Errorf("proc_pid_rusage pid %d failed (%d)", pid, int(result))
	}
	return uint64(readBytes), uint64(writeBytes), nil
}
