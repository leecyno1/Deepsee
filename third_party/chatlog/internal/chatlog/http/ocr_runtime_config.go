package http

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/pkg/util"
)

func (s *Service) configuredOCR() conf.OCRConfig {
	if s == nil || s.conf == nil {
		return conf.DefaultOCRConfig()
	}
	if provider, ok := s.conf.(OCRConfig); ok {
		return conf.NormalizeOCRConfig(provider.GetOCRConfig())
	}
	return conf.DefaultOCRConfig()
}

func (s *Service) ocrIndexPath() string {
	root := strings.TrimSpace(s.conf.GetWorkDir())
	if root == "" {
		runtimeRoot := strings.TrimSpace(s.conf.GetRuntimeDir())
		if accountProvider, ok := s.conf.(interface{ GetAccount() string }); ok {
			if account := strings.TrimSpace(accountProvider.GetAccount()); account != "" {
				root = util.WorkDirAt(runtimeRoot, account)
			}
		}
		if root == "" {
			root = runtimeRoot
		}
	}
	if root == "" {
		root = os.TempDir()
	}
	return filepath.Join(root, "ocr", "image_ocr.db")
}
