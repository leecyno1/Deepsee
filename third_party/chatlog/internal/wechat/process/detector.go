package process

import (
	"github.com/sjzar/chatlog/internal/wechat/model"
)

type Detector interface {
	FindProcesses() ([]*model.Process, error)
}
