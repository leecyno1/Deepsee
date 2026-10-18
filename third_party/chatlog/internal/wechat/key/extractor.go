package key

import (
	"context"

	"github.com/sjzar/chatlog/internal/wechat/model"
)

// Extractor 定义密钥提取器接口
type Extractor interface {
	// Extract 从进程中提取密钥
	// dataKey, imgKey, error
	Extract(ctx context.Context, proc *model.Process) (string, string, error)

	// SearchKey 在内存中搜索密钥
	SearchKey(ctx context.Context, memory []byte) (string, bool)
}
