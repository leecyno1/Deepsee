package wechat

import (
	"context"

	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/internal/wechat/backend"
	"github.com/sjzar/chatlog/internal/wechat/model"
	"github.com/sjzar/chatlog/internal/wechat/process"
)

// Manager 微信管理器
type Manager struct {
	detector   process.Detector
	backend    backend.Backend
	accounts   []*Account
	processMap map[string]*model.Process
}

// NewManager 创建新的微信管理器
func NewManager(platform backend.Backend) *Manager {
	return &Manager{
		detector:   platform.Detector(),
		backend:    platform,
		accounts:   make([]*Account, 0),
		processMap: make(map[string]*model.Process),
	}
}

// Load 加载微信进程信息
func (m *Manager) Load() error {
	// 查找微信进程
	processes, err := m.detector.FindProcesses()
	if err != nil {
		return err
	}

	// 转换为账号信息
	accounts := make([]*Account, 0, len(processes))
	processMap := make(map[string]*model.Process, len(processes))

	for _, p := range processes {
		account := newAccount(p, m)

		accounts = append(accounts, account)
		if account.Name != "" {
			processMap[account.Name] = p
		}
	}

	m.accounts = accounts
	m.processMap = processMap

	return nil
}

// GetAccount 获取指定名称的账号
func (m *Manager) GetAccount(name string) (*Account, error) {
	p, err := m.GetProcess(name)
	if err != nil {
		return nil, err
	}
	return newAccount(p, m), nil
}

func (m *Manager) GetProcess(name string) (*model.Process, error) {
	p, ok := m.processMap[name]
	if !ok {
		return nil, errors.WeChatAccountNotFound(name)
	}
	return p, nil
}

// GetAccounts 获取所有账号
func (m *Manager) GetAccounts() []*Account {
	return m.accounts
}

// GetAllProcesses 获取所有微信进程
func (m *Manager) GetAllProcesses() ([]*model.Process, error) {
	// 重新加载进程信息
	if err := m.Load(); err != nil {
		return nil, err
	}

	// 从processMap中提取所有进程
	processes := make([]*model.Process, 0, len(m.processMap))
	for _, p := range m.processMap {
		processes = append(processes, p)
	}

	return processes, nil
}

// DecryptDatabase 便捷方法：通过账号名解密数据库
func (m *Manager) DecryptDatabase(ctx context.Context, accountName, dbPath, outputPath string) error {
	// 获取账号
	account, err := m.GetAccount(accountName)
	if err != nil {
		return err
	}

	// 使用账号解密数据库
	return account.DecryptDatabase(ctx, dbPath, outputPath)
}
