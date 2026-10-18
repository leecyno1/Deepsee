package chatlog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	chatlogapp "github.com/sjzar/chatlog/internal/chatlog"
	clog "github.com/sjzar/chatlog/pkg/log"
)

type actionEvent struct {
	Type      string      `json:"type"`
	Action    string      `json:"action,omitempty"`
	Stage     string      `json:"stage,omitempty"`
	Message   string      `json:"message,omitempty"`
	ErrorCode string      `json:"error_code,omitempty"`
	Data      interface{} `json:"data,omitempty"`
	Timestamp string      `json:"timestamp"`
}

func emitActionEvent(eventType, action, stage, message, errorCode string, data interface{}) {
	payload := actionEvent{
		Type:      eventType,
		Action:    action,
		Stage:     stage,
		Message:   message,
		ErrorCode: errorCode,
		Data:      data,
		Timestamp: time.Now().Format(time.RFC3339),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stdout, "{\"type\":\"error\",\"message\":%q,\"timestamp\":%q}\n", err.Error(), time.Now().Format(time.RFC3339))
		return
	}
	fmt.Fprintln(os.Stdout, string(encoded))
}

var (
	opsPID     int
	opsAccount string

	setHTTPAddr         string
	setWorkDir          string
	setDataKey          string
	setImageKey         string
	setDataDir          string
	setLogRetentionDays int
)

var opsCmd = &cobra.Command{
	Use:   "ops",
	Short: "面向 LLM 和脚本的 JSON Lines 运维接口",
}

func init() {
	rootCmd.AddCommand(opsCmd)
	opsCmd.AddCommand(
		opsStatusCmd,
		opsAccountsCmd,
		opsImageKeyCmd,
		opsDatabaseKeyCmd,
		opsServerStartCmd,
		opsConfigSetCmd,
		opsAccountSelectCmd,
	)
	for _, command := range []*cobra.Command{
		opsStatusCmd,
		opsImageKeyCmd,
		opsDatabaseKeyCmd,
		opsServerStartCmd,
		opsAccountSelectCmd,
	} {
		addTargetFlags(command)
	}
	opsConfigSetCmd.Flags().StringVar(&setHTTPAddr, "http-addr", "", "HTTP 监听地址")
	opsConfigSetCmd.Flags().StringVar(&setWorkDir, "work-dir", "", "工作目录")
	opsConfigSetCmd.Flags().StringVar(&setDataKey, "data-key", "", "数据库密钥")
	opsConfigSetCmd.Flags().StringVar(&setImageKey, "image-key", "", "图片密钥")
	opsConfigSetCmd.Flags().StringVar(&setDataDir, "data-dir", "", "微信数据目录")
	opsConfigSetCmd.Flags().IntVar(&setLogRetentionDays, "log-retention-days", 0, "日志保留天数（1-365）")
}

var opsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "输出当前状态",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := initSelectedActionManager("status")
		if err != nil {
			return err
		}
		emitActionEvent("success", "status", "completed", "已输出当前状态", "", manager.Snapshot())
		return nil
	},
}

var opsAccountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "列出运行中与历史账号",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := initActionManager()
		if err != nil {
			return emitActionFailure("accounts", "init_failed", err)
		}
		emitActionEvent("success", "accounts", "completed", "已输出账号列表", "", manager.ListAccounts())
		return nil
	},
}

var opsImageKeyCmd = &cobra.Command{
	Use:   "image-key",
	Short: "获取图片解密密钥",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := initSelectedActionManager("image-key")
		if err != nil {
			return err
		}
		emitActionEvent("action_started", "image-key", "starting", "开始获取图片密钥", "", manager.Snapshot())
		err = manager.GetImageKeyWithStatus(actionProgress("image-key"))
		if err != nil {
			return emitActionFailure("image-key", "get_image_key_failed", err)
		}
		emitActionEvent("success", "image-key", "completed", "已获取图片密钥", "", manager.Snapshot())
		return nil
	},
}

var opsDatabaseKeyCmd = &cobra.Command{
	Use:   "database-key",
	Short: "重启微信并获取数据库密钥",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := initSelectedActionManager("database-key")
		if err != nil {
			return err
		}
		emitActionEvent("action_started", "database-key", "starting", "开始获取数据库密钥", "", manager.Snapshot())
		err = manager.RestartAndGetDataKey(actionProgress("database-key"))
		if err != nil {
			return emitActionFailure("database-key", "restart_and_get_key_failed", err)
		}
		emitActionEvent("success", "database-key", "completed", "已获取数据库密钥", "", manager.Snapshot())
		return nil
	},
}

var opsServerStartCmd = &cobra.Command{
	Use:   "server-start",
	Short: "启动 HTTP 服务并保持运行",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := initSelectedActionManager("server-start")
		if err != nil {
			return err
		}
		emitActionEvent("action_started", "server-start", "starting", "正在启动 HTTP 服务", "", manager.Snapshot())
		if err := manager.StartService(); err != nil {
			return emitActionFailure("server-start", "start_http_failed", err)
		}
		emitActionEvent("success", "server-start", "running", "HTTP 服务已启动", "", manager.Snapshot())
		waitForActionSignal(func() {
			started := time.Now()
			emitActionEvent("state", "server-start", "stopping", "正在停止 HTTP 和数据库服务", "", nil)
			if err := manager.StopService(); err != nil {
				log.Error().Err(err).Msg("stop action HTTP service failed")
				emitActionEvent("error", "server-start", "stop_failed", err.Error(), "stop_failed", map[string]any{
					"elapsed_ms": time.Since(started).Milliseconds(),
				})
				return
			}
			emitActionEvent("success", "server-start", "stopped", "HTTP 服务及数据库资源已完全退出", "", map[string]any{
				"elapsed_ms": time.Since(started).Milliseconds(),
			})
		})
		return nil
	},
}

var opsConfigSetCmd = &cobra.Command{
	Use:   "config-set",
	Short: "更新运行配置",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := initActionManager()
		if err != nil {
			return emitActionFailure("config-set", "init_failed", err)
		}
		var retention *int
		if cmd.Flags().Changed("log-retention-days") {
			if setLogRetentionDays < clog.MinRetentionDays || setLogRetentionDays > clog.MaxRetentionDays {
				return emitActionFailure("config-set", "invalid_args", fmt.Errorf("日志保留天数必须在 %d-%d 之间", clog.MinRetentionDays, clog.MaxRetentionDays))
			}
			value := setLogRetentionDays
			retention = &value
		}
		err = manager.SetConfigValues(
			setHTTPAddr,
			setWorkDir,
			setDataKey,
			setImageKey,
			setDataDir,
			retention,
		)
		if err != nil {
			return emitActionFailure("config-set", "set_failed", err)
		}
		emitActionEvent("success", "config-set", "completed", "配置已更新", "", manager.Snapshot())
		return nil
	},
}

var opsAccountSelectCmd = &cobra.Command{
	Use:   "account-select",
	Short: "切换当前账号",
	RunE: func(cmd *cobra.Command, args []string) error {
		if opsPID == 0 && opsAccount == "" {
			return emitActionFailure("account-select", "invalid_args", fmt.Errorf("必须提供 --pid 或 --account"))
		}
		manager, err := initActionManager()
		if err != nil {
			return emitActionFailure("account-select", "init_failed", err)
		}
		emitActionEvent("action_started", "account-select", "starting", "开始切换账号", "", nil)
		if err := manager.SwitchToAccount(opsPID, opsAccount); err != nil {
			return emitActionFailure("account-select", "switch_account_failed", err)
		}
		emitActionEvent("success", "account-select", "completed", "账号切换完成", "", manager.Snapshot())
		return nil
	},
}

func addTargetFlags(command *cobra.Command) {
	command.Flags().IntVar(&opsPID, "pid", 0, "目标微信 PID")
	command.Flags().StringVar(&opsAccount, "account", "", "账号标识（历史账号名）")
}

func initActionManager() (*chatlogapp.Manager, error) {
	manager := chatlogapp.New()
	if err := manager.InitAction(""); err != nil {
		return nil, err
	}
	return manager, nil
}

func initSelectedActionManager(action string) (*chatlogapp.Manager, error) {
	manager, err := initActionManager()
	if err != nil {
		return nil, emitActionFailure(action, "init_failed", err)
	}
	if err := manager.SelectAccount(opsPID, opsAccount); err != nil {
		return nil, emitActionFailure(action, "select_account_failed", err)
	}
	return manager, nil
}

func actionProgress(action string) func(string) {
	return func(message string) {
		emitActionEvent("state", action, "progress", message, "", nil)
	}
}

func emitActionFailure(action, code string, err error) error {
	emitActionEvent("error", action, code, err.Error(), code, nil)
	return err
}

func waitForActionSignal(cleanup func()) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	if cleanup != nil {
		cleanup()
	}
}
