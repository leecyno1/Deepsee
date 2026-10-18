package ports

import (
	"context"
	"time"

	"github.com/sjzar/chatlog/internal/model"
)

// MessageChangeBatch is one ordered page read from a single conversation by
// the database coordinator. Consumers receive the same immutable message
// pointers and keep their own durable business cursors.
type MessageChangeBatch struct {
	Sequence   uint64           `json:"sequence"`
	Talker     string           `json:"talker"`
	Messages   []*model.Message `json:"-"`
	DetectedAt time.Time        `json:"detected_at"`
}

type MessageChangeHandler func(context.Context, MessageChangeBatch) error

// MessageChangeFeed exposes the shared incremental stream. since is used only
// for the short in-memory replay window when a consumer joins after startup.
type MessageChangeFeed interface {
	SubscribeMessageChanges(name string, since time.Time, handler MessageChangeHandler) (func(), error)
	MessageChangeStats() MessageChangeStats
}

type MessageChangeStats struct {
	Running            bool   `json:"running"`
	Mode               string `json:"mode"`
	Subscribers        int    `json:"subscribers"`
	BufferedBatches    int    `json:"buffered_batches"`
	BufferedMessages   int    `json:"buffered_messages"`
	PendingDeliveries  int    `json:"pending_deliveries"`
	PendingSourceFiles int    `json:"pending_source_files"`
	FilesystemEvents   uint64 `json:"filesystem_events"`
	Scans              uint64 `json:"scans"`
	TargetedScans      uint64 `json:"targeted_scans"`
	GenericScans       uint64 `json:"generic_scans"`
	TargetedTalkers    uint64 `json:"targeted_talkers"`
	SessionQueries     uint64 `json:"session_queries"`
	MessageQueries     uint64 `json:"message_queries"`
	PublishedBatches   uint64 `json:"published_batches"`
	PublishedMessages  uint64 `json:"published_messages"`
	DeliveryRetries    uint64 `json:"delivery_retries"`
	SuppressedWarnings uint64 `json:"suppressed_warnings"`
	LastEventAt        string `json:"last_event_at,omitempty"`
	LastScanAt         string `json:"last_scan_at,omitempty"`
	LastPublishAt      string `json:"last_publish_at,omitempty"`
	LastScanDurationMS int64  `json:"last_scan_duration_ms"`
	LastLagMS          int64  `json:"last_lag_ms"`
	LastError          string `json:"last_error,omitempty"`
}
