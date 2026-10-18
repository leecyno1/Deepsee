package main

import (
	"fmt"
	"log"
	"os"

	"github.com/sjzar/chatlog/cmd/chatlog"
	"github.com/sjzar/chatlog/internal/buildprofile"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	if err := buildprofile.EnsureGoRun(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := chatlog.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
