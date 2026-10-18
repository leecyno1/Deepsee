//go:build cgo

package wcdbapi

/*
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

typedef long long sqlite3_int64;
typedef unsigned long long sqlite3_uint64;
typedef struct sqlite3_file sqlite3_file;
typedef struct sqlite3_io_methods sqlite3_io_methods;
typedef struct sqlite3_vfs sqlite3_vfs;
typedef void (*sqlite3_syscall_ptr)(void);

struct sqlite3_file {
  const sqlite3_io_methods *pMethods;
};

struct sqlite3_io_methods {
  int iVersion;
  int (*xClose)(sqlite3_file*);
  int (*xRead)(sqlite3_file*, void*, int iAmt, sqlite3_int64 iOfst);
  int (*xWrite)(sqlite3_file*, const void*, int iAmt, sqlite3_int64 iOfst);
  int (*xTruncate)(sqlite3_file*, sqlite3_int64 size);
  int (*xSync)(sqlite3_file*, int flags);
  int (*xFileSize)(sqlite3_file*, sqlite3_int64 *pSize);
  int (*xLock)(sqlite3_file*, int);
  int (*xUnlock)(sqlite3_file*, int);
  int (*xCheckReservedLock)(sqlite3_file*, int *pResOut);
  int (*xFileControl)(sqlite3_file*, int op, void *pArg);
  int (*xSectorSize)(sqlite3_file*);
  int (*xDeviceCharacteristics)(sqlite3_file*);
  int (*xShmMap)(sqlite3_file*, int iPg, int pgsz, int, void volatile**);
  int (*xShmLock)(sqlite3_file*, int offset, int n, int flags);
  void (*xShmBarrier)(sqlite3_file*);
  int (*xShmUnmap)(sqlite3_file*, int deleteFlag);
  int (*xFetch)(sqlite3_file*, sqlite3_int64 iOfst, int iAmt, void **pp);
  int (*xUnfetch)(sqlite3_file*, sqlite3_int64 iOfst, void *p);
};

struct sqlite3_vfs {
  int iVersion;
  int szOsFile;
  int mxPathname;
  sqlite3_vfs *pNext;
  const char *zName;
  void *pAppData;
  int (*xOpen)(sqlite3_vfs*, const char *zName, sqlite3_file*, int flags, int *pOutFlags);
  int (*xDelete)(sqlite3_vfs*, const char *zName, int syncDir);
  int (*xAccess)(sqlite3_vfs*, const char *zName, int flags, int *pResOut);
  int (*xFullPathname)(sqlite3_vfs*, const char *zName, int nOut, char *zOut);
  void *(*xDlOpen)(sqlite3_vfs*, const char *zFilename);
  void (*xDlError)(sqlite3_vfs*, int nByte, char *zErrMsg);
  void (*(*xDlSym)(sqlite3_vfs*,void*, const char *zSymbol))(void);
  void (*xDlClose)(sqlite3_vfs*, void*);
  int (*xRandomness)(sqlite3_vfs*, int nByte, char *zOut);
  int (*xSleep)(sqlite3_vfs*, int microseconds);
  int (*xCurrentTime)(sqlite3_vfs*, double*);
  int (*xGetLastError)(sqlite3_vfs*, int, char*);
  int (*xCurrentTimeInt64)(sqlite3_vfs*, sqlite3_int64*);
  int (*xSetSystemCall)(sqlite3_vfs*, const char*, sqlite3_syscall_ptr);
  sqlite3_syscall_ptr (*xGetSystemCall)(sqlite3_vfs*, const char*);
  const char *(*xNextSystemCall)(sqlite3_vfs*, const char*);
};

extern sqlite3_vfs *sqlite3_vfs_find(const char *zVfsName);
extern int sqlite3_vfs_register(sqlite3_vfs*, int makeDflt);

extern int goChatlogDirectAcquire(sqlite3_int64 handle);
extern void goChatlogDirectRelease(sqlite3_int64 handle);
extern int goChatlogDirectRead(sqlite3_int64 handle, void *buffer, int amount, sqlite3_int64 offset);
extern int goChatlogDirectSize(sqlite3_int64 handle, sqlite3_int64 *size);

#define SQLITE_OK 0
#define SQLITE_ERROR 1
#define SQLITE_PERM 3
#define SQLITE_READONLY 8
#define SQLITE_IOERR 10
#define SQLITE_CANTOPEN 14
#define SQLITE_NOTFOUND 12
#define SQLITE_IOERR_SHORT_READ (SQLITE_IOERR | (2<<8))
#define SQLITE_OPEN_READONLY 0x00000001
#define SQLITE_OPEN_READWRITE 0x00000002
#define SQLITE_OPEN_CREATE 0x00000004
#define SQLITE_IOCAP_IMMUTABLE 0x00002000
#define SQLITE_FCNTL_VFSNAME 12

typedef struct ChatlogDirectFile {
  sqlite3_file base;
  sqlite3_int64 handle;
} ChatlogDirectFile;

static int chatlog_direct_close(sqlite3_file *file) {
  ChatlogDirectFile *direct = (ChatlogDirectFile*)file;
  if (direct->handle > 0) {
    goChatlogDirectRelease(direct->handle);
    direct->handle = 0;
  }
  file->pMethods = NULL;
  return SQLITE_OK;
}

static int chatlog_direct_read(sqlite3_file *file, void *buffer, int amount, sqlite3_int64 offset) {
  ChatlogDirectFile *direct = (ChatlogDirectFile*)file;
  return goChatlogDirectRead(direct->handle, buffer, amount, offset);
}

static int chatlog_direct_write(sqlite3_file *file, const void *buffer, int amount, sqlite3_int64 offset) {
  (void)file; (void)buffer; (void)amount; (void)offset;
  return SQLITE_READONLY;
}
static int chatlog_direct_truncate(sqlite3_file *file, sqlite3_int64 size) {
  (void)file; (void)size; return SQLITE_READONLY;
}
static int chatlog_direct_sync(sqlite3_file *file, int flags) {
  (void)file; (void)flags; return SQLITE_OK;
}
static int chatlog_direct_size(sqlite3_file *file, sqlite3_int64 *size) {
  ChatlogDirectFile *direct = (ChatlogDirectFile*)file;
  return goChatlogDirectSize(direct->handle, size);
}
static int chatlog_direct_lock(sqlite3_file *file, int lock) {
  (void)file; (void)lock; return SQLITE_OK;
}
static int chatlog_direct_unlock(sqlite3_file *file, int lock) {
  (void)file; (void)lock; return SQLITE_OK;
}
static int chatlog_direct_reserved(sqlite3_file *file, int *result) {
  (void)file; *result = 0; return SQLITE_OK;
}
static int chatlog_direct_control(sqlite3_file *file, int op, void *arg) {
  (void)file; (void)op; (void)arg; return SQLITE_NOTFOUND;
}
static int chatlog_direct_sector(sqlite3_file *file) {
  (void)file; return 4096;
}
static int chatlog_direct_characteristics(sqlite3_file *file) {
  (void)file; return SQLITE_IOCAP_IMMUTABLE;
}

static const sqlite3_io_methods chatlog_direct_io = {
  1,
  chatlog_direct_close,
  chatlog_direct_read,
  chatlog_direct_write,
  chatlog_direct_truncate,
  chatlog_direct_sync,
  chatlog_direct_size,
  chatlog_direct_lock,
  chatlog_direct_unlock,
  chatlog_direct_reserved,
  chatlog_direct_control,
  chatlog_direct_sector,
  chatlog_direct_characteristics,
  NULL, NULL, NULL, NULL, NULL, NULL
};

static sqlite3_int64 chatlog_direct_parse_handle(const char *name) {
  const char *marker;
  char *end = NULL;
  unsigned long long value;
  if (name == NULL) return 0;
  marker = strstr(name, "chatlog-direct-");
  if (marker == NULL) return 0;
  marker += strlen("chatlog-direct-");
  value = strtoull(marker, &end, 10);
  if (end == marker || value == 0 || *end != '\0') return 0;
  return (sqlite3_int64)value;
}

static int chatlog_direct_open(sqlite3_vfs *vfs, const char *name, sqlite3_file *file, int flags, int *outFlags) {
  ChatlogDirectFile *direct = (ChatlogDirectFile*)file;
  sqlite3_int64 handle = chatlog_direct_parse_handle(name);
  (void)vfs;
  memset(direct, 0, sizeof(*direct));
  if (handle <= 0 || (flags & (SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE)) != 0) {
    return SQLITE_CANTOPEN;
  }
  if (goChatlogDirectAcquire(handle) != SQLITE_OK) {
    return SQLITE_CANTOPEN;
  }
  direct->handle = handle;
  direct->base.pMethods = &chatlog_direct_io;
  if (outFlags != NULL) *outFlags = SQLITE_OPEN_READONLY;
  return SQLITE_OK;
}

static int chatlog_direct_delete(sqlite3_vfs *vfs, const char *name, int syncDir) {
  (void)vfs; (void)name; (void)syncDir; return SQLITE_READONLY;
}
static int chatlog_direct_access(sqlite3_vfs *vfs, const char *name, int flags, int *result) {
  (void)vfs; (void)flags;
  *result = chatlog_direct_parse_handle(name) > 0 ? 1 : 0;
  return SQLITE_OK;
}
static int chatlog_direct_fullpath(sqlite3_vfs *vfs, const char *name, int nOut, char *out) {
  size_t length;
  (void)vfs;
  if (name == NULL || nOut <= 0) return SQLITE_CANTOPEN;
  length = strlen(name);
  if (length >= (size_t)nOut) length = (size_t)nOut - 1;
  memcpy(out, name, length);
  out[length] = '\0';
  return SQLITE_OK;
}

static sqlite3_vfs chatlog_direct_vfs;
static int chatlog_direct_registered = 0;

static int chatlog_register_direct_vfs(void) {
  sqlite3_vfs *parent;
  if (chatlog_direct_registered) return SQLITE_OK;
  parent = sqlite3_vfs_find(NULL);
  if (parent == NULL) return SQLITE_ERROR;
  memset(&chatlog_direct_vfs, 0, sizeof(chatlog_direct_vfs));
  chatlog_direct_vfs.iVersion = 1;
  chatlog_direct_vfs.szOsFile = sizeof(ChatlogDirectFile);
  chatlog_direct_vfs.mxPathname = 1024;
  chatlog_direct_vfs.zName = "chatlog_direct";
  chatlog_direct_vfs.pAppData = parent;
  chatlog_direct_vfs.xOpen = chatlog_direct_open;
  chatlog_direct_vfs.xDelete = chatlog_direct_delete;
  chatlog_direct_vfs.xAccess = chatlog_direct_access;
  chatlog_direct_vfs.xFullPathname = chatlog_direct_fullpath;
  chatlog_direct_vfs.xDlOpen = parent->xDlOpen;
  chatlog_direct_vfs.xDlError = parent->xDlError;
  chatlog_direct_vfs.xDlSym = parent->xDlSym;
  chatlog_direct_vfs.xDlClose = parent->xDlClose;
  chatlog_direct_vfs.xRandomness = parent->xRandomness;
  chatlog_direct_vfs.xSleep = parent->xSleep;
  chatlog_direct_vfs.xCurrentTime = parent->xCurrentTime;
  chatlog_direct_vfs.xGetLastError = parent->xGetLastError;
  if (sqlite3_vfs_register(&chatlog_direct_vfs, 0) != SQLITE_OK) return SQLITE_ERROR;
  chatlog_direct_registered = 1;
  return SQLITE_OK;
}
*/
import "C"

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"
)

const (
	directSQLitePrefix  = "chatlog-direct:"
	directSQLiteVFSName = "chatlog_direct"
)

type directVFSReader interface {
	ReadAt([]byte, int64) (int, error)
	Size() int64
	Close() error
}

type directVFSRegistration struct {
	reader directVFSReader
	refs   int
}

var directVFSRegistry = struct {
	sync.Mutex
	next    atomic.Uint64
	readers map[uint64]*directVFSRegistration
}{readers: make(map[uint64]*directVFSRegistration)}

var (
	directVFSRegisterOnce sync.Once
	directVFSRegisterErr  error
)

func ensureDirectVFSRegistered() error {
	directVFSRegisterOnce.Do(func() {
		if result := int(C.chatlog_register_direct_vfs()); result != 0 {
			directVFSRegisterErr = fmt.Errorf("register direct SQLite VFS: result %d", result)
		}
	})
	return directVFSRegisterErr
}

func registerDirectVFSReader(reader directVFSReader) (uint64, string, error) {
	if reader == nil {
		return 0, "", fmt.Errorf("direct SQLite reader is nil")
	}
	if err := ensureDirectVFSRegistered(); err != nil {
		return 0, "", err
	}
	handle := directVFSRegistry.next.Add(1)
	directVFSRegistry.Lock()
	directVFSRegistry.readers[handle] = &directVFSRegistration{reader: reader, refs: 1}
	directVFSRegistry.Unlock()
	return handle, fmt.Sprintf("%s%d", directSQLitePrefix, handle), nil
}

func releaseDirectVFSOwner(handle uint64) {
	directVFSRelease(handle)
}

func directVFSRelease(handle uint64) {
	var closeReader directVFSReader
	directVFSRegistry.Lock()
	registration := directVFSRegistry.readers[handle]
	if registration != nil {
		registration.refs--
		if registration.refs <= 0 {
			delete(directVFSRegistry.readers, handle)
			closeReader = registration.reader
		}
	}
	directVFSRegistry.Unlock()
	if closeReader != nil {
		_ = closeReader.Close()
	}
}

func directVFSLookup(handle uint64) directVFSReader {
	directVFSRegistry.Lock()
	registration := directVFSRegistry.readers[handle]
	var reader directVFSReader
	if registration != nil {
		reader = registration.reader
	}
	directVFSRegistry.Unlock()
	return reader
}

func directHandleFromPath(path string) (uint64, bool) {
	if !isDirectSQLitePath(path) {
		return 0, false
	}
	handle, err := strconv.ParseUint(strings.TrimPrefix(path, directSQLitePrefix), 10, 64)
	return handle, err == nil && handle > 0
}

// acquireDirectSQLiteLease closes the small gap between ensureDirectRead
// returning a token and SQLite's xOpen taking its own reference. A concurrent
// source refresh can therefore retire the prior owner without invalidating a
// query that is just about to open it.
func acquireDirectSQLiteLease(path string) (func(), error) {
	handle, direct := directHandleFromPath(path)
	if !direct {
		return func() {}, nil
	}
	directVFSRegistry.Lock()
	registration := directVFSRegistry.readers[handle]
	if registration == nil {
		directVFSRegistry.Unlock()
		return nil, fmt.Errorf("direct SQLite snapshot is no longer registered")
	}
	registration.refs++
	directVFSRegistry.Unlock()
	return func() { directVFSRelease(handle) }, nil
}

func validateDirectSQLitePath(path string) error {
	handle, direct := directHandleFromPath(path)
	if !direct {
		return nil
	}
	reader := directVFSLookup(handle)
	snapshot, ok := reader.(*directSnapshot)
	if !ok || snapshot == nil {
		return fmt.Errorf("direct SQLite snapshot is no longer registered")
	}
	if !snapshot.unchanged() {
		return errDirectSnapshotChanged
	}
	return nil
}

//export goChatlogDirectAcquire
func goChatlogDirectAcquire(handle C.sqlite3_int64) C.int {
	directVFSRegistry.Lock()
	registration := directVFSRegistry.readers[uint64(handle)]
	if registration == nil {
		directVFSRegistry.Unlock()
		return C.int(14)
	}
	registration.refs++
	directVFSRegistry.Unlock()
	return C.int(0)
}

//export goChatlogDirectRelease
func goChatlogDirectRelease(handle C.sqlite3_int64) {
	directVFSRelease(uint64(handle))
}

//export goChatlogDirectRead
func goChatlogDirectRead(handle C.sqlite3_int64, buffer unsafe.Pointer, amount C.int, offset C.sqlite3_int64) C.int {
	reader := directVFSLookup(uint64(handle))
	if reader == nil || amount < 0 || offset < 0 {
		return C.int(10)
	}
	destination := unsafe.Slice((*byte)(buffer), int(amount))
	n, err := reader.ReadAt(destination, int64(offset))
	if n < len(destination) {
		clear(destination[n:])
	}
	if err != nil {
		if int64(offset)+int64(n) >= reader.Size() {
			return C.int(10 | (2 << 8))
		}
		return C.int(10)
	}
	if n != len(destination) {
		return C.int(10 | (2 << 8))
	}
	return C.int(0)
}

//export goChatlogDirectSize
func goChatlogDirectSize(handle C.sqlite3_int64, size *C.sqlite3_int64) C.int {
	reader := directVFSLookup(uint64(handle))
	if reader == nil || size == nil {
		return C.int(10)
	}
	*size = C.sqlite3_int64(reader.Size())
	return C.int(0)
}

func isDirectSQLitePath(path string) bool {
	return strings.HasPrefix(path, directSQLitePrefix) && len(path) > len(directSQLitePrefix)
}

func directSQLiteDSN(path string) string {
	handle := path[len(directSQLitePrefix):]
	return fmt.Sprintf("file:chatlog-direct-%s?mode=ro&immutable=1&vfs=%s&_query_only=1", handle, directSQLiteVFSName)
}
