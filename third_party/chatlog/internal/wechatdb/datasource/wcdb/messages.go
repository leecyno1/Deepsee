package wcdb

import (
	"context"
	stderrors "errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/pkg/util"
)

func (ds *DataSource) GetMessages(ctx context.Context, startTime, endTime time.Time, talker, sender, keyword string, limit, offset int) ([]*model.Message, error) {
	if talker == "" {
		return nil, errors.ErrTalkerEmpty
	}

	var regex *regexp.Regexp
	var err error
	if keyword != "" {
		regex, err = regexp.Compile(keyword)
		if err != nil {
			return nil, errors.QueryFailed("invalid regex pattern", err)
		}
	}
	senders := util.Str2List(sender, ",")
	talkers := util.Str2List(talker, ",")
	if len(talkers) == 0 {
		return nil, errors.ErrTalkerEmpty
	}

	since := int64(0)
	until := int64(0)
	if !startTime.IsZero() {
		since = startTime.Unix()
	}
	if !endTime.IsZero() {
		until = endTime.Unix()
	}

	perTalkerLimit := 0
	if limit > 0 {
		perTalkerLimit = messageQueryFetchLimit(limit, offset, sender != "" || keyword != "")
	}

	out := make([]*model.Message, 0, perTalkerLimit*len(talkers))
	dedup := make(map[string]struct{}, perTalkerLimit*len(talkers))
	queryErrors := make([]error, 0)

	for _, tk := range talkers {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		rows, err := ds.client.GetMessagesInRange(tk, since, until, perTalkerLimit, 0)
		if err != nil {
			queryErrors = append(queryErrors, fmt.Errorf("%s: %w", tk, err))
			continue
		}

		for _, row := range rows {
			msgV4 := messageV4FromRow(row)
			msg := msgV4.Wrap(tk)
			if msg == nil {
				continue
			}
			if !startTime.IsZero() && msg.Time.Before(startTime) {
				continue
			}
			if !endTime.IsZero() && msg.Time.After(endTime) {
				continue
			}
			if len(senders) > 0 {
				match := false
				for _, s := range senders {
					if msg.Sender == s {
						match = true
						break
					}
				}
				if !match {
					continue
				}
			}
			if regex != nil && !regex.MatchString(msg.PlainTextContent()) {
				continue
			}
			dedupKey := fmt.Sprintf("%s:%d", msg.Talker, msg.Seq)
			if _, ok := dedup[dedupKey]; ok {
				continue
			}
			dedup[dedupKey] = struct{}{}
			out = append(out, msg)
		}
	}
	if len(queryErrors) != 0 {
		return nil, fmt.Errorf("message query incomplete: %w", stderrors.Join(queryErrors...))
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Seq < out[j].Seq
	})
	if limit > 0 {
		if offset >= len(out) {
			return []*model.Message{}, nil
		}
		end := offset + limit
		if end > len(out) {
			end = len(out)
		}
		return out[offset:end], nil
	}
	if offset > 0 && offset < len(out) {
		return out[offset:], nil
	}
	return out, nil
}

func (ds *DataSource) GetMessagesAfter(ctx context.Context, talker string, cursor model.MessageCursor, limit int) ([]*model.Message, error) {
	if strings.TrimSpace(talker) == "" {
		return nil, errors.ErrTalkerEmpty
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	rows, err := ds.client.GetMessagesAfter(talker, cursor, limit)
	if err != nil {
		return nil, err
	}
	return messagesAfterRows(talker, cursor, limit, rows), nil
}

func (ds *DataSource) GetMessagesAfterFiles(
	ctx context.Context,
	talker string,
	cursor model.MessageCursor,
	limit int,
	changedFiles []string,
) ([]*model.Message, error) {
	if strings.TrimSpace(talker) == "" {
		return nil, errors.ErrTalkerEmpty
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	rows, err := ds.client.GetMessagesAfterFiles(talker, cursor, limit, changedFiles)
	if err != nil {
		return nil, err
	}
	return messagesAfterRows(talker, cursor, limit, rows), nil
}

func (ds *DataSource) ResolveChangedMessageTalkers(
	ctx context.Context,
	changedFiles []string,
	candidates map[string]model.MessageCursor,
) ([]string, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return ds.client.ResolveChangedMessageTalkers(changedFiles, candidates)
}

func messagesAfterRows(
	talker string,
	cursor model.MessageCursor,
	limit int,
	rows []map[string]interface{},
) []*model.Message {
	out := make([]*model.Message, 0, len(rows))
	for _, row := range rows {
		messageV4 := messageV4FromRow(row)
		msg := messageV4.Wrap(talker)
		if msg == nil || !cursor.BeforeMessage(msg) {
			continue
		}
		out = append(out, msg)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Time.Equal(out[j].Time) {
			return out[i].DBLocalID < out[j].DBLocalID
		}
		return out[i].Time.Before(out[j].Time)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func messageQueryFetchLimit(limit, offset int, hasPostFilter bool) int {
	if limit <= 0 {
		return 0
	}
	if offset < 0 {
		offset = 0
	}
	const maxRequestedBeforeMultiplier = 50000 / 5
	if offset >= maxRequestedBeforeMultiplier || limit >= maxRequestedBeforeMultiplier-offset {
		return 50000
	}
	fetchLimit := (limit + offset) * 5
	minimum := 20
	if hasPostFilter {
		minimum = 1000
	}
	if fetchLimit < minimum {
		fetchLimit = minimum
	}
	if fetchLimit > 50000 {
		fetchLimit = 50000
	}
	return fetchLimit
}

func (ds *DataSource) GetMessage(ctx context.Context, talker string, seq int64) (*model.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	row, err := ds.client.GetMessage(talker, seq)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.ErrMessageNotFound
	}
	message := messageV4FromRow(row)
	return message.Wrap(talker), nil
}

func messageV4FromRow(row map[string]interface{}) model.MessageV4 {
	return model.MessageV4{
		LocalID:         toInt64(row["local_id"]),
		SortSeq:         toInt64(row["sort_seq"]),
		ServerID:        toInt64(row["server_id"]),
		LocalType:       toInt64(row["local_type"]),
		UserName:        toString(row["user_name"]),
		CreateTime:      toInt64(row["create_time"]),
		MessageContent:  toBytes(row["message_content"]),
		CompressContent: toBytes(row["compress_content"]),
		Source:          toBytes(row["source"]),
		PackedInfoData:  toBytes(row["packed_info_data"]),
		Status:          toInt64(row["status"]),
	}
}
