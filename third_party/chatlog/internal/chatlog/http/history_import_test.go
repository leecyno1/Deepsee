package http

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
)

type historySessionsDB struct{ Database }

func (d *historySessionsDB) GetSessions(string, int, int) (*ports.Sessions, error) {
	rows := make([]*model.Session, 503)
	for i := range rows {
		rows[i] = &model.Session{UserName: fmt.Sprintf("friend-%03d", i)}
	}
	return &ports.Sessions{Items: rows}, nil
}
func (d *historySessionsDB) GetContact(string) (*model.Contact, error) { return nil, nil }

func TestHistoryImportSessionsBeyondFirst500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Service{db: &historySessionsDB{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/sessions?limit=500&offset=500", nil)
	s.handleSessions(c)
	var result struct {
		Sessions []struct {
			Username string `json:"username"`
		} `json:"sessions"`
		Total   int  `json:"total"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || result.Total != 503 || len(result.Sessions) != 3 || result.HasMore || result.Sessions[0].Username != "friend-500" {
		t.Fatalf("incorrect second page: %s", w.Body.String())
	}
}

func TestHistoryImportPreservesWechatServerID(t *testing.T) {
	message := (&model.MessageV4{LocalID: 7, ServerID: 8123089943839909378,
		CreateTime: time.Now().Unix(), LocalType: 1, MessageContent: []byte("hello")}).Wrap("friend")
	row := toHistoryMessageOut(toHistoryMessage(message, ""))
	if row.ServerID != "8123089943839909378" {
		t.Fatalf("server id lost: %q", row.ServerID)
	}
}
