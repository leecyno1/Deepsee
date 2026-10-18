package chatlog

import (
	"testing"

	"github.com/sjzar/chatlog/pkg/version"
)

func TestRootVersionMatchesBuildVersion(t *testing.T) {
	if rootCmd.Version == "" {
		t.Fatal("root command version is empty")
	}
	if rootCmd.Version != version.Version {
		t.Fatalf("root version = %q, build version = %q", rootCmd.Version, version.Version)
	}
}
