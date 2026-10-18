package chatlog

import (
	"fmt"
	"os"

	"github.com/sjzar/chatlog/internal/chatlog"
	"github.com/sjzar/chatlog/pkg/process"
	"github.com/sjzar/chatlog/pkg/util"
	"github.com/sjzar/chatlog/pkg/version"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func init() {
	cobra.MousetrapHelpText = ""

	rootCmd.PersistentFlags().BoolVar(&Debug, "debug", false, "debug")
	rootCmd.PersistentPreRun = initLog
	rootCmd.Version = version.Version
	rootCmd.SetVersionTemplate("{{.Version}}\n")
}

func Execute() error {
	defer closeLogFile()
	return rootCmd.Execute()
}

var rootCmd = &cobra.Command{
	Use:   "chatlog",
	Short: "chatlog",
	Long:  `chatlog`,
	Example: `chatlog api catalog
chatlog api call sessions --param limit=20
chatlog ops status`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.MinimumNArgs(0),
	CompletionOptions: cobra.CompletionOptions{
		HiddenDefaultCmd: true,
	},
	PreRun: initTuiLog,
	Run:    Root,
}

func Root(cmd *cobra.Command, args []string) {
	cleanup := initSingleInstance()
	defer cleanup()

	m := chatlog.New()
	if err := m.Run(""); err != nil {
		log.Err(err).Msg("failed to run chatlog instance")
	}
}

func initSingleInstance() func() {
	cleanup, err := process.CheckSingleInstance(util.DefaultWorkDir(""))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	return cleanup
}
