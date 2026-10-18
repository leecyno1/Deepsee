package usecase

import (
	"errors"
	"testing"
)

type serverDatabaseFixture struct {
	startErrors   []error
	startCalls    int
	errorMessages []string
}

func (d *serverDatabaseFixture) Start() error {
	index := d.startCalls
	d.startCalls++
	if index < len(d.startErrors) {
		return d.startErrors[index]
	}
	return nil
}
func (d *serverDatabaseFixture) SetError(message string) {
	d.errorMessages = append(d.errorMessages, message)
}

func TestServerStartsDirectDatabaseOnce(t *testing.T) {
	db := &serverDatabaseFixture{}
	server := NewServer(db, nil, nil)

	server.startDatabase()

	if db.startCalls != 1 || len(db.errorMessages) != 0 {
		t.Fatalf("starts=%d errors=%v", db.startCalls, db.errorMessages)
	}
}

func TestServerPublishesFinalDatabaseFailure(t *testing.T) {
	db := &serverDatabaseFixture{startErrors: []error{errors.New("direct open failed")}}
	server := NewServer(db, nil, nil)

	server.startDatabase()

	if db.startCalls != 1 || len(db.errorMessages) != 1 || db.errorMessages[0] != "direct open failed" {
		t.Fatalf("database errors = %v", db.errorMessages)
	}
}
