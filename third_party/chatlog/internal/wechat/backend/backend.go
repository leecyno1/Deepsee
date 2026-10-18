package backend

import (
	"github.com/sjzar/chatlog/internal/wechat/decrypt"
	"github.com/sjzar/chatlog/internal/wechat/key"
	"github.com/sjzar/chatlog/internal/wechat/process"
)

type Identity interface {
	ID() string
}

type DetectorProvider interface {
	Detector() process.Detector
}

type ExtractorFactory interface {
	NewExtractor(version int) (key.Extractor, error)
}

type DecryptorFactory interface {
	NewDecryptor(version int) (decrypt.Decryptor, error)
}

// Backend composes focused platform capabilities. Consumers may depend on the
// narrow capability interfaces above when they do not need the complete set.
type Backend interface {
	Identity
	DetectorProvider
	ExtractorFactory
	DecryptorFactory
}
