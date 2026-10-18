package wcdbapi

import (
	"container/list"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const directPageCacheLimit = 2048

var errDirectSnapshotChanged = errors.New("encrypted database changed during direct query")

var errDirectSnapshotInvalid = errors.New("encrypted database snapshot has an invalid SQLite header")

type directFileFingerprint struct {
	exists bool
	size   int64
	mtime  int64
}

type directSnapshotFingerprint struct {
	source directFileFingerprint
	wal    directFileFingerprint
}

type directPageCacheEntry struct {
	pageNo uint32
	data   []byte
}

// directSnapshot is an immutable SQLite view assembled from the encrypted
// main database and the last committed transaction in its WAL. Main pages are
// decrypted lazily; committed WAL pages are held encrypted and decrypted only
// when SQLite requests them.
type directSnapshot struct {
	path        string
	file        *os.File
	fingerprint directSnapshotFingerprint
	encKey      []byte
	walPages    map[uint32][]byte
	walState    directWALState
	logicalSize int64

	cacheMu sync.Mutex
	cache   map[uint32]*list.Element
	lru     *list.List
}

type directClientSnapshot struct {
	fingerprint directSnapshotFingerprint
	handle      uint64
	token       string
	snapshot    *directSnapshot
}

type directWALFrame struct {
	pageNo uint32
	data   []byte
}

type directWALState struct {
	fingerprint directFileFingerprint
	salt1       uint32
	salt2       uint32
	checksum1   uint32
	checksum2   uint32
	littleSum   bool
	parsedSize  int64
	dbSize      uint32
	committed   map[uint32][]byte
	pending     []directWALFrame
}

func directFileStat(path string) (directFileFingerprint, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return directFileFingerprint{}, nil
		}
		return directFileFingerprint{}, err
	}
	if !info.Mode().IsRegular() {
		return directFileFingerprint{}, fmt.Errorf("database path is not a regular file: %s", path)
	}
	return directFileFingerprint{exists: true, size: info.Size(), mtime: info.ModTime().UnixNano()}, nil
}

func directInputFingerprint(path string) (directSnapshotFingerprint, error) {
	source, err := directFileStat(path)
	if err != nil {
		return directSnapshotFingerprint{}, err
	}
	if !source.exists {
		return directSnapshotFingerprint{}, os.ErrNotExist
	}
	wal, err := directFileStat(path + "-wal")
	if err != nil {
		return directSnapshotFingerprint{}, err
	}
	return directSnapshotFingerprint{source: source, wal: wal}, nil
}

func (fingerprint directSnapshotFingerprint) matches(path string) bool {
	current, err := directInputFingerprint(path)
	return err == nil && current == fingerprint
}

func (snapshot *directSnapshot) Size() int64 { return snapshot.logicalSize }

func (snapshot *directSnapshot) Close() error {
	if snapshot.file == nil {
		return nil
	}
	err := snapshot.file.Close()
	snapshot.file = nil
	return err
}

func (snapshot *directSnapshot) unchanged() bool {
	return snapshot != nil && snapshot.fingerprint.matches(snapshot.path)
}

func (snapshot *directSnapshot) ReadAt(destination []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("negative direct database offset: %d", offset)
	}
	if len(destination) == 0 {
		return 0, nil
	}
	if offset >= snapshot.logicalSize {
		return 0, io.EOF
	}
	available := snapshot.logicalSize - offset
	wanted := len(destination)
	if int64(wanted) > available {
		wanted = int(available)
	}
	written := 0
	for written < wanted {
		absolute := offset + int64(written)
		pageNo := uint32(absolute/int64(pageSize)) + 1
		pageOffset := int(absolute % int64(pageSize))
		page, err := snapshot.decodedPage(pageNo)
		if err != nil {
			return written, err
		}
		count := min(wanted-written, pageSize-pageOffset)
		copy(destination[written:written+count], page[pageOffset:pageOffset+count])
		written += count
	}
	if written < len(destination) {
		return written, io.EOF
	}
	return written, nil
}

func (snapshot *directSnapshot) decodedPage(pageNo uint32) ([]byte, error) {
	snapshot.cacheMu.Lock()
	if element := snapshot.cache[pageNo]; element != nil {
		snapshot.lru.MoveToFront(element)
		page := element.Value.(*directPageCacheEntry).data
		snapshot.cacheMu.Unlock()
		return page, nil
	}
	snapshot.cacheMu.Unlock()

	encrypted, fromWAL, err := snapshot.encryptedPage(pageNo)
	if err != nil {
		return nil, err
	}
	var decoded []byte
	if fromWAL {
		decoded, err = decryptWALPage(snapshot.encKey, encrypted, pageNo)
	} else {
		decoded, err = decryptPageRaw(snapshot.encKey, encrypted, int(pageNo))
	}
	if err != nil {
		return nil, fmt.Errorf("decrypt direct database page %d: %w", pageNo, err)
	}

	snapshot.cacheMu.Lock()
	if element := snapshot.cache[pageNo]; element != nil {
		snapshot.lru.MoveToFront(element)
		decoded = element.Value.(*directPageCacheEntry).data
	} else {
		element := snapshot.lru.PushFront(&directPageCacheEntry{pageNo: pageNo, data: decoded})
		snapshot.cache[pageNo] = element
		if snapshot.lru.Len() > directPageCacheLimit {
			oldest := snapshot.lru.Back()
			if oldest != nil {
				snapshot.lru.Remove(oldest)
				delete(snapshot.cache, oldest.Value.(*directPageCacheEntry).pageNo)
			}
		}
	}
	snapshot.cacheMu.Unlock()
	return decoded, nil
}

func (snapshot *directSnapshot) encryptedPage(pageNo uint32) ([]byte, bool, error) {
	if page := snapshot.walPages[pageNo]; page != nil {
		return page, true, nil
	}
	offset := int64(pageNo-1) * int64(pageSize)
	page := make([]byte, pageSize)
	n, err := snapshot.file.ReadAt(page, offset)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	if n != pageSize {
		return nil, false, fmt.Errorf("short encrypted database page %d: %d bytes", pageNo, n)
	}
	return page, false, nil
}

func newDirectSnapshot(path, keyHex string, newDecryptor DecryptorFactory, previous *directSnapshot) (*directSnapshot, error) {
	path = filepath.Clean(path)
	dataKey, err := decodeHexKey(keyHex)
	if err != nil {
		return nil, err
	}
	decryptor, err := newDecryptor()
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		before, err := directInputFingerprint(path)
		if err != nil {
			return nil, err
		}
		if before.source.size < pageSize || before.source.size%pageSize != 0 {
			return nil, fmt.Errorf("invalid encrypted database size: %d", before.source.size)
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		firstEncrypted := make([]byte, pageSize)
		if _, err := io.ReadFull(file, firstEncrypted); err != nil {
			_ = file.Close()
			return nil, err
		}
		if !decryptor.Validate(firstEncrypted, dataKey) {
			_ = file.Close()
			return nil, fmt.Errorf("data key does not match encrypted database: %s", path)
		}
		encKey, _, err := decryptor.DeriveKeys(dataKey, firstEncrypted[:saltSize])
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		firstDecoded, err := decryptPageRaw(encKey, firstEncrypted, 1)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		logicalSize := before.source.size
		if pages := binary.BigEndian.Uint32(firstDecoded[28:32]); pages > 0 && int64(pages)*pageSize <= before.source.size {
			logicalSize = int64(pages) * pageSize
		}
		var previousWAL *directWALState
		if previous != nil {
			previousWAL = &previous.walState
		}
		walState, err := readCommittedDirectWAL(path+"-wal", before.wal, previousWAL)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if walState.dbSize > 0 {
			logicalSize = int64(walState.dbSize) * pageSize
		}
		after, err := directInputFingerprint(path)
		if err != nil || before != after {
			_ = file.Close()
			continue
		}
		snapshot := &directSnapshot{
			path:        path,
			file:        file,
			fingerprint: before,
			encKey:      encKey,
			walPages:    walState.committed,
			walState:    walState,
			logicalSize: logicalSize,
			cache:       make(map[uint32]*list.Element),
			lru:         list.New(),
		}
		// A committed WAL frame may replace page 1 after the main page was
		// validated. Decode the page SQLite will actually see before publishing
		// the VFS token so a main/WAL race never escapes as SQLITE_NOTADB.
		firstVisible, err := snapshot.decodedPage(1)
		if err == nil {
			err = validateDirectSQLiteHeader(firstVisible, logicalSize)
		}
		if err != nil {
			lastErr = err
			_ = snapshot.Close()
			continue
		}
		if !snapshot.unchanged() {
			lastErr = errDirectSnapshotChanged
			_ = snapshot.Close()
			continue
		}
		return snapshot, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errDirectSnapshotChanged
}

func validateDirectSQLiteHeader(page []byte, logicalSize int64) error {
	if len(page) < 100 || string(page[:16]) != "SQLite format 3\x00" {
		return errDirectSnapshotInvalid
	}
	pageBytes := int64(binary.BigEndian.Uint16(page[16:18]))
	if pageBytes == 1 {
		pageBytes = 65536
	}
	if pageBytes != pageSize || logicalSize < pageBytes || logicalSize%pageBytes != 0 {
		return fmt.Errorf("%w: page_size=%d logical_size=%d", errDirectSnapshotInvalid, pageBytes, logicalSize)
	}
	return nil
}

func readCommittedDirectWAL(path string, expected directFileFingerprint, previous *directWALState) (directWALState, error) {
	empty := directWALState{fingerprint: expected, parsedSize: walHeaderSize, committed: make(map[uint32][]byte)}
	if !expected.exists || expected.size < walHeaderSize {
		return empty, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return directWALState{}, err
	}
	defer file.Close()
	header := make([]byte, walHeaderSize)
	if _, err := io.ReadFull(file, header); err != nil {
		return directWALState{}, err
	}
	walPageSize := binary.BigEndian.Uint32(header[8:12])
	if walPageSize == 1 {
		walPageSize = 65536
	}
	if walPageSize != pageSize {
		return directWALState{}, fmt.Errorf("unsupported WAL page size: %d", walPageSize)
	}
	magic := binary.BigEndian.Uint32(header[0:4])
	if magic != 0x377f0682 && magic != 0x377f0683 {
		return directWALState{}, fmt.Errorf("invalid WAL magic: 0x%08x", magic)
	}
	littleSum := magic == 0x377f0682
	headerSum1, headerSum2 := directWALChecksum(header[:24], littleSum, 0, 0)
	if headerSum1 != binary.BigEndian.Uint32(header[24:28]) || headerSum2 != binary.BigEndian.Uint32(header[28:32]) {
		return directWALState{}, fmt.Errorf("invalid WAL header checksum")
	}
	salt1 := binary.BigEndian.Uint32(header[16:20])
	salt2 := binary.BigEndian.Uint32(header[20:24])
	state := directWALState{
		fingerprint: expected,
		salt1:       salt1,
		salt2:       salt2,
		checksum1:   headerSum1,
		checksum2:   headerSum2,
		littleSum:   littleSum,
		parsedSize:  walHeaderSize,
		committed:   make(map[uint32][]byte),
	}
	// A WAL with unchanged salts resumes after the last verified frame. This
	// also covers WeChat's preallocated WAL files whose physical size stays
	// constant while the stale next slot is overwritten. If an uncommitted tail
	// was observed, rebuild from frame 1 because SQLite may rewrite that tail.
	if previous != nil && previous.fingerprint.exists &&
		previous.salt1 == salt1 && previous.salt2 == salt2 &&
		previous.littleSum == littleSum &&
		previous.parsedSize >= walHeaderSize &&
		(expected == previous.fingerprint ||
			expected.size > previous.fingerprint.size ||
			(expected.size == previous.fingerprint.size && len(previous.pending) == 0)) &&
		expected.size >= previous.parsedSize {
		state.parsedSize = previous.parsedSize
		state.dbSize = previous.dbSize
		state.checksum1 = previous.checksum1
		state.checksum2 = previous.checksum2
		state.committed = cloneDirectWALPages(previous.committed)
		state.pending = append([]directWALFrame(nil), previous.pending...)
	}

	frameSize := int64(walFrameHdr + pageSize)
	frame := make([]byte, frameSize)
	for offset := state.parsedSize; offset+frameSize <= expected.size; offset += frameSize {
		if _, err := file.ReadAt(frame, offset); err != nil {
			return directWALState{}, err
		}
		frameHeader := frame[:walFrameHdr]
		pageNo := binary.BigEndian.Uint32(frameHeader[0:4])
		if pageNo == 0 || pageNo > 1_000_000 ||
			binary.BigEndian.Uint32(frameHeader[8:12]) != salt1 ||
			binary.BigEndian.Uint32(frameHeader[12:16]) != salt2 {
			break
		}
		checksum1, checksum2 := directWALChecksum(frameHeader[:8], littleSum, state.checksum1, state.checksum2)
		checksum1, checksum2 = directWALChecksum(frame[walFrameHdr:], littleSum, checksum1, checksum2)
		if checksum1 != binary.BigEndian.Uint32(frameHeader[16:20]) || checksum2 != binary.BigEndian.Uint32(frameHeader[20:24]) {
			break
		}
		state.checksum1 = checksum1
		state.checksum2 = checksum2
		page := make([]byte, pageSize)
		copy(page, frame[walFrameHdr:])
		state.pending = append(state.pending, directWALFrame{pageNo: pageNo, data: page})
		if commitSize := binary.BigEndian.Uint32(frameHeader[4:8]); commitSize != 0 {
			for _, pending := range state.pending {
				state.committed[pending.pageNo] = pending.data
			}
			state.pending = state.pending[:0]
			state.dbSize = commitSize
		}
		state.parsedSize = offset + frameSize
	}
	return state, nil
}

func cloneDirectWALPages(source map[uint32][]byte) map[uint32][]byte {
	cloned := make(map[uint32][]byte, len(source))
	for pageNo, page := range source {
		cloned[pageNo] = page
	}
	return cloned
}

func directWALChecksum(data []byte, littleEndian bool, sum1, sum2 uint32) (uint32, uint32) {
	var order binary.ByteOrder = binary.BigEndian
	if littleEndian {
		order = binary.LittleEndian
	}
	for offset := 0; offset+8 <= len(data); offset += 8 {
		sum1 += order.Uint32(data[offset:offset+4]) + sum2
		sum2 += order.Uint32(data[offset+4:offset+8]) + sum1
	}
	return sum1, sum2
}

func (c *Client) ensureDirectRead(src string) (string, error) {
	if plain, err := isReadableSQLite(src); err == nil && plain {
		return src, nil
	}
	src = filepath.Clean(src)
	keyHex, err := c.resolveDataKey(src)
	if err != nil {
		return "", err
	}
	fingerprint, err := directInputFingerprint(src)
	if err != nil {
		return "", err
	}

	c.directMu.Lock()
	if c.directClosed {
		c.directMu.Unlock()
		return "", fmt.Errorf("direct database client is closed")
	}
	if current := c.direct[src]; current != nil && current.fingerprint == fingerprint {
		token := current.token
		c.directMu.Unlock()
		return token, nil
	}
	previous := c.direct[src]
	c.directMu.Unlock()

	var previousSnapshot *directSnapshot
	if previous != nil {
		previousSnapshot = previous.snapshot
	}
	snapshot, err := newDirectSnapshot(src, keyHex, c.newDecryptor, previousSnapshot)
	if err != nil {
		return "", err
	}
	handle, token, err := registerDirectVFSReader(snapshot)
	if err != nil {
		_ = snapshot.Close()
		return "", err
	}

	c.directMu.Lock()
	if c.directClosed {
		c.directMu.Unlock()
		releaseDirectVFSOwner(handle)
		return "", fmt.Errorf("direct database client is closed")
	}
	if current := c.direct[src]; current != nil && current.fingerprint == snapshot.fingerprint {
		c.directMu.Unlock()
		releaseDirectVFSOwner(handle)
		return current.token, nil
	}
	previous = c.direct[src]
	c.direct[src] = &directClientSnapshot{fingerprint: snapshot.fingerprint, handle: handle, token: token, snapshot: snapshot}
	c.directMu.Unlock()
	if previous != nil {
		releaseDirectVFSOwner(previous.handle)
	}
	return token, nil
}

// invalidateDirectRead retires only the snapshot that produced an error. The
// token check prevents a slow failed query from discarding a newer snapshot
// already installed by another goroutine.
func (c *Client) invalidateDirectRead(src, token string) {
	src = filepath.Clean(src)
	c.directMu.Lock()
	current := c.direct[src]
	if current == nil || (token != "" && current.token != token) {
		c.directMu.Unlock()
		return
	}
	delete(c.direct, src)
	c.directMu.Unlock()
	releaseDirectVFSOwner(current.handle)
}

func (c *Client) Close() error {
	c.directMu.Lock()
	snapshots := c.direct
	c.direct = make(map[string]*directClientSnapshot)
	c.directClosed = true
	c.directMu.Unlock()
	for _, snapshot := range snapshots {
		if snapshot != nil {
			releaseDirectVFSOwner(snapshot.handle)
		}
	}
	return nil
}
