package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
	iwechat "github.com/sjzar/chatlog/internal/wechat"
)

type KeyService interface {
	GetDataKey(info *iwechat.Account) (string, error)
	GetImageKeyWithStatus(info *iwechat.Account, status func(string)) (string, error)
	GetWeChatInstances() []*iwechat.Account
}

type KeyState interface {
	CurrentAccount() *iwechat.Account
	SwitchCurrent(info *iwechat.Account)
	GetDataDir() string
	SetDataDir(dir string)
	GetVersion() int
	SetDataKey(key string)
	SetImgKey(key string)
	Refresh()
	UpdateConfig()
}

// Keys owns the data/image-key application workflows. UI and CLI managers
// delegate here instead of mixing platform workflow with transport lifecycle.
type Keys struct {
	state   KeyState
	service KeyService
	capture ports.KeyCapture
	media   ports.MediaKeys
}

func NewKeys(state KeyState, service KeyService, capture ports.KeyCapture, media ports.MediaKeys) *Keys {
	return &Keys{state: state, service: service, capture: capture, media: media}
}

func (u *Keys) GetDataKey() error {
	current := u.state.CurrentAccount()
	if current == nil {
		return fmt.Errorf("未选择任何账号")
	}
	if _, err := u.service.GetDataKey(current); err != nil {
		return err
	}
	u.persist()
	return nil
}

func (u *Keys) GetImageKey(onStatus func(string)) error {
	current := u.state.CurrentAccount()
	if current == nil {
		return fmt.Errorf("未选择任何账号")
	}
	if onStatus != nil {
		onStatus("正在优先尝试本地推导图片密钥")
	}
	imageKey, err := u.service.GetImageKeyWithStatus(current, onStatus)
	if err != nil && u.capture.IsImagePermissionError(err) {
		if onStatus != nil {
			onStatus("普通用户无法读取微信内存，准备请求临时管理员授权")
		}
		dataDir := firstNonEmpty(current.DataDir, u.state.GetDataDir())
		imageKey, err = u.capture.ExtractImageKey(
			context.Background(),
			current.PID,
			dataDir,
			onStatus,
		)
	}
	if err != nil {
		return err
	}
	if imageKey != "" {
		current.ImgKey = imageKey
		u.state.SetImgKey(imageKey)
		u.media.SetImageKey(imageKey)
		if dataDir := u.state.GetDataDir(); dataDir != "" {
			go u.media.RefreshXOR(dataDir)
		}
	}
	u.persist()
	if onStatus != nil {
		onStatus("图片密钥已保存；本次任务未保留管理员权限")
	}
	return nil
}

func (u *Keys) CaptureDataKey(onStatus func(string)) error {
	current := u.state.CurrentAccount()
	if current == nil {
		return fmt.Errorf("未选择任何账号")
	}
	if !u.capture.Available() {
		return fmt.Errorf("提取数据库密钥需要 Frida：请先执行 pip3 install frida-tools")
	}
	if onStatus != nil {
		onStatus("[1/6] 检查 Frida 环境")
	}

	exePath := current.ExePath
	dataDir := firstNonEmpty(current.DataDir, u.state.GetDataDir())
	log.Info().Msg("CaptureDataKey: Frida-only data key path")

	extractCtx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	capture, err := u.capture.CaptureDataKeys(extractCtx, dataDir, onStatus)
	if err != nil {
		return fmt.Errorf("Frida 提取密钥失败: %w", err)
	}

	newInstance := pickBestInstance(u.service.GetWeChatInstances(), exePath)
	if newInstance != nil && newInstance.DataDir != "" {
		dataDir = newInstance.DataDir
	}
	if dataDir == "" {
		deadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(deadline) {
			if best := pickBestInstance(u.service.GetWeChatInstances(), exePath); best != nil {
				newInstance = best
				if best.DataDir != "" {
					dataDir = best.DataDir
					break
				}
			}
			if onStatus != nil {
				onStatus("[6/6] 密钥已捕获，等待微信登录并准备数据目录")
			}
			time.Sleep(time.Second)
		}
	} else if onStatus != nil {
		onStatus("[6/6] 已找到账号数据目录，正在保存数据库密钥")
	}

	if newInstance != nil {
		u.state.SwitchCurrent(newInstance)
		current = newInstance
	}
	if dataDir != "" {
		if applyErr := u.capture.ApplyDataKeys(dataDir, capture, onStatus); applyErr != nil {
			log.Warn().Err(applyErr).Msg("apply frida key to all_keys.json failed")
		}
	}
	if current != nil {
		current.Key = capture.Value
		current.DataDir = dataDir
	}
	u.state.SetDataKey(capture.Value)
	if u.state.GetDataDir() == "" {
		u.state.SetDataDir(dataDir)
	}
	if currentDataDir := u.state.GetDataDir(); currentDataDir != "" {
		go u.media.RefreshXOR(currentDataDir)
	}
	u.persist()
	if onStatus != nil {
		onStatus("Frida 提取密钥成功")
	}
	log.Info().Msg("Successfully got data key via Frida")
	return nil
}

func (u *Keys) Inspect(pid int, force, showXORKey bool) (string, error) {
	instances := u.service.GetWeChatInstances()
	if len(instances) == 0 {
		return "", fmt.Errorf("wechat process not found")
	}

	var selected *iwechat.Account
	switch {
	case len(instances) == 1:
		selected = instances[0]
	case pid == 0:
		var output strings.Builder
		output.WriteString("Select a process:\n")
		for _, instance := range instances {
			fmt.Fprintf(&output, "PID: %d. %s[Version: %s Data Dir: %s ]\n",
				instance.PID, instance.Name, instance.FullVersion, instance.DataDir)
		}
		return output.String(), nil
	default:
		for _, instance := range instances {
			if instance.PID == uint32(pid) {
				selected = instance
				break
			}
		}
	}
	if selected == nil {
		return "", fmt.Errorf("wechat process not found")
	}
	current := u.state.CurrentAccount()
	if current == nil || current.PID != selected.PID {
		u.state.SwitchCurrent(selected)
	}

	dataKey, imageKey := selected.Key, selected.ImgKey
	if dataKey == "" || imageKey == "" || force {
		var err error
		dataKey, imageKey, err = selected.GetKey(context.Background())
		if err != nil {
			return "", err
		}
		u.persist()
	}
	result := fmt.Sprintf("Data Key: [%s]\nImage Key: [%s]", dataKey, imageKey)
	if u.state.GetVersion() == 4 && showXORKey {
		if xorKey, err := u.media.RefreshXOR(u.state.GetDataDir()); err == nil {
			result += fmt.Sprintf("\nXor Key: [0x%X]", xorKey)
		}
	}
	return result, nil
}

func (u *Keys) persist() {
	u.state.Refresh()
	u.state.UpdateConfig()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func pickBestInstance(instances []*iwechat.Account, exePath string) *iwechat.Account {
	var best *iwechat.Account
	for _, instance := range instances {
		if instance == nil || (exePath != "" && instance.ExePath != exePath) {
			continue
		}
		if best == nil {
			best = instance
			continue
		}
		instanceHasData := instance.DataDir != ""
		bestHasData := best.DataDir != ""
		if (instanceHasData && !bestHasData) ||
			(instanceHasData == bestHasData && instance.PID > best.PID) {
			best = instance
		}
	}
	return best
}
