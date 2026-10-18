package hermespush

import (
	"os"
	"testing"
	"time"
)

func TestHermesBridgeAssetUsesStableContentAddressedPath(t *testing.T) {
	t.Setenv("CHATLOG_DIR", t.TempDir())
	path, cleanup, err := materializeHermesBridgeAsset("fixture", "print('ok')\n")
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	firstMTime := info.ModTime()
	time.Sleep(10 * time.Millisecond)
	again, cleanupAgain, err := materializeHermesBridgeAsset("fixture", "print('ok')\n")
	if err != nil {
		t.Fatal(err)
	}
	cleanupAgain()
	if again != path {
		t.Fatalf("bridge path changed: %q -> %q", path, again)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(firstMTime) {
		t.Fatalf("unchanged bridge was rewritten: %s -> %s", firstMTime, info.ModTime())
	}
}
