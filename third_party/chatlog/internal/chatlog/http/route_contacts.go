package http

import (
	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/errors"
)

func (s *Service) initAddressBookRoutes(api *gin.RouterGroup) {
	api.GET("/contacts", s.handleContacts)
	api.GET("/openim/:wxid/corp", s.handleOpenimCorp)
	api.GET("/chatrooms", s.handleChatRooms)
}

func (s *Service) handleContacts(c *gin.Context) {
	q := struct {
		Query    string `form:"query"`
		Limit    int    `form:"limit"`
		Offset   int    `form:"offset"`
		IsFriend string `form:"is_friend"`
	}{}
	if err := c.ShouldBindQuery(&q); err != nil {
		errors.Err(c, errors.InvalidArg("query"))
		return
	}
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 500, 1, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	q.Limit = limit
	q.Offset, err = parseDatabaseQueryInteger(c.Query("offset"), 0, 0, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("offset"))
		return
	}
	isFriendFilter, err := parseOptionalBool(q.IsFriend)
	if err != nil {
		errors.Err(c, errors.InvalidArg("is_friend"))
		return
	}
	fetchLimit, fetchOffset := q.Limit, q.Offset
	if isFriendFilter != nil {
		// Apply friend filter before pagination.
		fetchLimit, fetchOffset = 0, 0
	}
	list, err := s.db.GetContacts(q.Query, fetchLimit, fetchOffset)
	if err != nil {
		errors.Err(c, err)
		return
	}
	out := make([]gin.H, 0, len(list.Items))
	for _, ct := range list.Items {
		if isFriendFilter != nil && ct.IsFriend != *isFriendFilter {
			continue
		}
		display := ct.DisplayName()
		if display == "" {
			display = ct.UserName
		}
		out = append(out, gin.H{
			"username":  ct.UserName,
			"alias":     ct.Alias,
			"remark":    ct.Remark,
			"nickname":  ct.NickName,
			"display":   display,
			"is_friend": ct.IsFriend,
		})
	}
	if isFriendFilter != nil {
		out = paginateRows(out, q.Limit, q.Offset)
	}
	writeJSON(c, gin.H{"count": len(out), "contacts": out})
}

func (s *Service) handleChatRooms(c *gin.Context) {
	q := struct {
		Query  string `form:"query"`
		Limit  int    `form:"limit"`
		Offset int    `form:"offset"`
	}{}
	if err := c.ShouldBindQuery(&q); err != nil {
		errors.Err(c, errors.InvalidArg("query"))
		return
	}
	var err error
	q.Limit, err = parseDatabaseQueryInteger(c.Query("limit"), 500, 1, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	q.Offset, err = parseDatabaseQueryInteger(c.Query("offset"), 0, 0, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("offset"))
		return
	}
	list, err := s.db.GetChatRooms(q.Query, q.Limit, q.Offset)
	if err != nil {
		errors.Err(c, err)
		return
	}
	out := make([]gin.H, 0, len(list.Items))
	for _, room := range list.Items {
		display := room.DisplayName()
		if display == "" {
			display = room.Name
		}
		out = append(out, gin.H{
			"name":       room.Name,
			"remark":     room.Remark,
			"nickname":   room.NickName,
			"display":    display,
			"owner":      room.Owner,
			"user_count": len(room.Users),
		})
	}
	writeJSON(c, gin.H{"count": len(out), "chatrooms": out})
}
