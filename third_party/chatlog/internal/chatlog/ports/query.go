// Package ports contains transport- and storage-neutral application contracts.
package ports

import "github.com/sjzar/chatlog/internal/model"

type Contacts struct {
	Items []*model.Contact `json:"items"`
}

type ChatRooms struct {
	Items []*model.ChatRoom `json:"items"`
}

type Sessions struct {
	Items []*model.Session `json:"items"`
}
