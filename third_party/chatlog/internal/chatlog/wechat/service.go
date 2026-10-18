package wechat

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sjzar/chatlog/internal/wechat"
	"github.com/sjzar/chatlog/internal/wechat/key"
)

type Service struct {
	accounts AccountProvider
}

type AccountProvider interface {
	Load() error
	GetAccounts() []*wechat.Account
}

func NewService(accounts AccountProvider) *Service {
	return &Service{accounts: accounts}
}

// GetWeChatInstances returns all running WeChat instances
func (s *Service) GetWeChatInstances() []*wechat.Account {
	instances, _ := s.GetWeChatInstancesWithError()
	return instances
}

func (s *Service) GetWeChatInstancesWithError() ([]*wechat.Account, error) {
	if s.accounts == nil {
		return nil, fmt.Errorf("wechat account manager is not configured")
	}
	if err := s.accounts.Load(); err != nil {
		return nil, err
	}
	return s.accounts.GetAccounts(), nil
}

// GetDataKey extracts the encryption key from a WeChat process
func (s *Service) GetDataKey(info *wechat.Account) (string, error) {
	if info == nil {
		return "", fmt.Errorf("no WeChat instance selected")
	}

	ctx := key.WithRequest(context.Background(), key.Request{DataOnly: true})
	key, _, err := info.GetKey(ctx)
	if err != nil {
		return "", err
	}

	return key, nil
}

// GetImageKey extracts the image key from a WeChat process
func (s *Service) GetImageKey(info *wechat.Account) (string, error) {
	return s.GetImageKeyWithStatus(info, nil)
}

// GetImageKeyWithStatus extracts the image key and forwards detailed progress.
func (s *Service) GetImageKeyWithStatus(info *wechat.Account, status func(string)) (string, error) {
	if info == nil {
		return "", fmt.Errorf("no WeChat instance selected")
	}

	ctx := context.Background()
	if status != nil {
		ctx = key.WithRequest(ctx, key.Request{Status: status})
	}
	return info.GetImageKey(ctx)
}

// RefreshImageKey selects the running account that owns dataDir and refreshes
// its image key. HTTP depends on this small capability instead of constructing
// another WeChat service.
func (s *Service) RefreshImageKey(dataDir string) (string, error) {
	instances, err := s.GetWeChatInstancesWithError()
	if err != nil {
		return "", err
	}
	if len(instances) == 0 {
		return "", fmt.Errorf("wechat instance unavailable")
	}

	target := instances[0]
	cleanDataDir := filepath.Clean(strings.TrimSpace(dataDir))
	for _, instance := range instances {
		instanceDir := strings.TrimSpace(instance.DataDir)
		if instanceDir == "" {
			continue
		}
		cleanInstanceDir := filepath.Clean(instanceDir)
		if strings.Contains(cleanDataDir, cleanInstanceDir) || strings.Contains(cleanInstanceDir, cleanDataDir) {
			target = instance
			break
		}
	}
	return s.GetImageKey(target)
}
