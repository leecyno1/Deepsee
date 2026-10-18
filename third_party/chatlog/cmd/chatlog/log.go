package chatlog

import (
	"io"
	"os"
	"path/filepath"
	"time"

	clog "github.com/sjzar/chatlog/pkg/log"
	"github.com/sjzar/chatlog/pkg/observability"
	"github.com/sjzar/chatlog/pkg/util"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var Debug bool

func initLog(_ *cobra.Command, _ []string) {
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	if Debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	logWriter := clog.Init(logFilePath(), clog.DefaultRetentionDays)

	writers := []io.Writer{zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}}
	if logWriter != nil {
		writers = append(writers, zerolog.ConsoleWriter{Out: logWriter, NoColor: true, TimeFormat: time.RFC3339})
	}
	writers = append(writers, observability.LogWriter())

	log.Logger = log.Output(io.MultiWriter(writers...))
}

func initTuiLog(_ *cobra.Command, _ []string) {
	logOutput := io.Writer(io.Discard)
	if writer := clog.Init(logFilePath(), clog.DefaultRetentionDays); writer != nil {
		logOutput = writer
	}

	log.Logger = log.Output(io.MultiWriter(
		zerolog.ConsoleWriter{Out: logOutput, NoColor: true, TimeFormat: time.RFC3339},
		observability.LogWriter(),
	))
	logrus.SetOutput(logOutput)
}

func logFilePath() string {
	logDir := filepath.Join(util.DefaultWorkDir(""), "log")
	_ = util.PrepareDir(logDir)
	return filepath.Join(logDir, "chatlog.log")
}

func closeLogFile() {
	_ = clog.Close()
}
