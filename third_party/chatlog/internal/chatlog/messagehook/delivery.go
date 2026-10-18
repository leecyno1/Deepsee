package messagehook

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/hermespush"
	"github.com/sjzar/chatlog/internal/model"
)

type activeDelivery struct {
	cancel context.CancelFunc
}

func deliveryJobKey(eventID, target string) string {
	return eventID + "\x00" + target
}

func (s *Service) beginActiveDelivery(parent context.Context, job deliveryJob) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	active := &activeDelivery{cancel: cancel}
	key := deliveryJobKey(job.EventID, job.Target)
	s.activeDeliveryMu.Lock()
	if s.activeDeliveries == nil {
		s.activeDeliveries = make(map[string]*activeDelivery)
	}
	s.activeDeliveries[key] = active
	s.activeDeliveryMu.Unlock()
	return ctx, func() {
		s.activeDeliveryMu.Lock()
		if s.activeDeliveries[key] == active {
			delete(s.activeDeliveries, key)
		}
		s.activeDeliveryMu.Unlock()
		cancel()
	}
}

func (s *Service) cancelActiveDeliveries(eventID, target string) {
	var cancels []context.CancelFunc
	s.activeDeliveryMu.Lock()
	for key, active := range s.activeDeliveries {
		if active == nil {
			continue
		}
		if target != "" {
			if key != deliveryJobKey(eventID, target) {
				continue
			}
		} else if !strings.HasPrefix(key, eventID+"\x00") {
			continue
		}
		cancels = append(cancels, active.cancel)
	}
	s.activeDeliveryMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func deliveryTargets(cfg *conf.MessageHook) []deliveryTarget {
	if cfg == nil {
		return nil
	}
	targets, ok := conf.ParseHookNotifyTargets(cfg.NotifyMode)
	if !ok {
		targets = conf.HookNotifyTargets{Post: true}
	}
	out := make([]deliveryTarget, 0, 3)
	if targets.Post {
		out = append(out, deliveryTarget{Name: "post", Destination: strings.TrimSpace(cfg.PostURL)})
	}
	if targets.Weixin {
		out = append(out, deliveryTarget{Name: "weixin"})
	}
	if targets.QQ {
		out = append(out, deliveryTarget{Name: "qq"})
	}
	return out
}

func (s *Service) runDeliveryWorker(ctx context.Context) {
	for {
		if err := s.drainDeliveries(ctx); err != nil {
			s.setDeliveryError(err)
		}
		var timer *time.Timer
		var timerC <-chan time.Time
		next, exists, err := s.store.NextDeliveryAt()
		if err != nil {
			s.setDeliveryError(err)
		} else if exists {
			delay := time.Until(next)
			if delay <= 0 {
				continue
			}
			timer = time.NewTimer(delay)
			timerC = timer.C
		}
		select {
		case <-ctx.Done():
			stopDeliveryTimer(timer)
			return
		case <-s.deliveryWake:
			stopDeliveryTimer(timer)
		case <-timerC:
		}
	}
}

func stopDeliveryTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func (s *Service) wakeDeliveries() {
	// Wake enough workers to preserve delivery concurrency. A short channel in
	// tests naturally coalesces the extra notifications.
	for i := 0; i < deliveryWorkers; i++ {
		select {
		case s.deliveryWake <- struct{}{}:
		default:
			return
		}
	}
}

func (s *Service) drainDeliveries(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		job, err := s.store.Claim(time.Now())
		if err != nil {
			return err
		}
		if job == nil {
			return nil
		}
		deliveryContext, finishDelivery := s.beginActiveDelivery(ctx, *job)
		result := s.deliverJobContext(deliveryContext, *job)
		deliveryCanceled := deliveryContext.Err() != nil && ctx.Err() == nil
		finishDelivery()
		if ctx.Err() != nil {
			// Keep the claimed delivery durable for retry after restart instead
			// of recording project shutdown as a business delivery failure.
			return nil
		}
		if deliveryCanceled {
			// CancelEvent has already persisted the terminal canceled state.
			continue
		}
		if err := s.store.Complete(*job, result, time.Now()); err != nil {
			return err
		}
	}
}

func (s *Service) setDeliveryError(err error) {
	if err == nil {
		return
	}
	s.statsMu.Lock()
	s.lastScanError = "delivery: " + err.Error()
	s.statsMu.Unlock()
}

func (s *Service) deliverJob(job deliveryJob) DeliveryResult {
	return s.deliverJobContext(context.Background(), job)
}

func (s *Service) deliverJobContext(ctx context.Context, job deliveryJob) DeliveryResult {
	switch job.Target {
	case "post":
		if strings.TrimSpace(job.Destination) == "" {
			return DeliveryResult{Target: "post", Status: "failed", Detail: "post_url empty", Success: false}
		}
		return s.deliverPostWithKeyContext(ctx, job.Destination, job.Event, job.EventID)
	case "weixin":
		return s.deliverHermesWeixin(ctx, job.Event)
	case "qq":
		return s.deliverHermesQQ(ctx, job.Event)
	default:
		return DeliveryResult{Target: job.Target, Status: "failed", Detail: "unsupported target", Success: false}
	}
}

func (s *Service) eventTrigger(event Event) *model.Message {
	if s.db == nil || strings.TrimSpace(event.Talker) == "" || event.TriggerSeq == 0 {
		return nil
	}
	message, err := s.db.GetMessage(event.Talker, event.TriggerSeq)
	if err != nil {
		return nil
	}
	return message
}

func (s *Service) deliverHermesWeixin(ctx context.Context, event Event) DeliveryResult {
	cfg, err := hermespush.DiscoverWeixinConfig()
	if err != nil {
		return DeliveryResult{Target: "weixin", Status: "failed", Detail: err.Error(), Success: false}
	}
	mediaPaths, cleanup, mediaErr := s.resolveTriggerMediaContext(ctx, s.eventTrigger(event))
	defer removeDeliveryFiles(cleanup)
	if err := hermespush.SendWeixinContext(ctx, s.httpClient, cfg, hermespush.WeixinSendRequest{
		Text: buildWeixinMessage(event), MediaPaths: mediaPaths,
	}); err != nil {
		return DeliveryResult{Target: "weixin", Status: "failed", Detail: joinDeliveryError(err, mediaErr), Success: false}
	}
	return DeliveryResult{Target: "weixin", Status: "sent", Detail: deliveryMediaDetail(mediaPaths, mediaErr), Success: true}
}

func (s *Service) deliverHermesQQ(ctx context.Context, event Event) DeliveryResult {
	cfg, err := hermespush.DiscoverQQConfig()
	if err != nil {
		return DeliveryResult{Target: "qq", Status: "failed", Detail: err.Error(), Success: false}
	}
	mediaPaths, cleanup, mediaErr := s.resolveTriggerMediaContext(ctx, s.eventTrigger(event))
	defer removeDeliveryFiles(cleanup)
	if err := hermespush.SendQQContext(ctx, cfg, hermespush.QQSendRequest{
		Text: buildWeixinMessage(event), MediaPaths: mediaPaths,
	}); err != nil {
		return DeliveryResult{Target: "qq", Status: "failed", Detail: joinDeliveryError(err, mediaErr), Success: false}
	}
	return DeliveryResult{Target: "qq", Status: "sent", Detail: deliveryMediaDetail(mediaPaths, mediaErr), Success: true}
}

func removeDeliveryFiles(paths []string) {
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

func joinDeliveryError(deliveryErr, mediaErr error) string {
	if mediaErr == nil {
		return deliveryErr.Error()
	}
	return deliveryErr.Error() + "; media_resolve=" + mediaErr.Error()
}

func deliveryMediaDetail(paths []string, mediaErr error) string {
	if len(paths) > 0 {
		return fmt.Sprintf("media=%d", len(paths))
	}
	if mediaErr != nil {
		return "media_resolve_failed=" + mediaErr.Error()
	}
	return ""
}
