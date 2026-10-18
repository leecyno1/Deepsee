package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	maxConcurrentLocalMedia = 2
	maxLocalMediaFileSize   = 64 << 20
)

var errLocalMediaTooLarge = errors.New("local media file is too large")

func (s *Service) acquireLocalMediaSlot(ctx context.Context) (func(), error) {
	s.localMediaSlotOnce.Do(func() {
		s.localMediaSlots = make(chan struct{}, maxConcurrentLocalMedia)
	})
	select {
	case s.localMediaSlots <- struct{}{}:
		return func() { <-s.localMediaSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func readLocalMediaFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if info, statErr := file.Stat(); statErr == nil && info.Size() > maxLocalMediaFileSize {
		return nil, fmt.Errorf("%w: %d bytes", errLocalMediaTooLarge, info.Size())
	}

	data, err := io.ReadAll(io.LimitReader(file, maxLocalMediaFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxLocalMediaFileSize {
		return nil, fmt.Errorf("%w: more than %d bytes", errLocalMediaTooLarge, maxLocalMediaFileSize)
	}
	return data, nil
}
