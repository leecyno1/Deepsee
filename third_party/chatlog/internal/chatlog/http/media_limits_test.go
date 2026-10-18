package http

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadLocalMediaFileRejectsOversizeSparseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversize.dat")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxLocalMediaFileSize + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := readLocalMediaFile(path); !errors.Is(err, errLocalMediaTooLarge) {
		t.Fatalf("readLocalMediaFile error = %v, want size-limit error", err)
	}
}

func TestAcquireLocalMediaSlotHonorsCancellation(t *testing.T) {
	service := &Service{}
	releaseFirst, err := service.acquireLocalMediaSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFirst()
	releaseSecond, err := service.acquireLocalMediaSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSecond()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.acquireLocalMediaSlot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("third acquire error = %v, want context cancellation", err)
	}
}

func TestSNSWasmKeystreamRejectsOversizeBeforeAssetSetup(t *testing.T) {
	service := &Service{}
	if _, err := service.getSNSWasmKeystream(context.Background(), "key", maxSNSWasmKeystreamSize+1, "raw"); err == nil {
		t.Fatal("oversize WASM keystream request unexpectedly succeeded")
	}
}
