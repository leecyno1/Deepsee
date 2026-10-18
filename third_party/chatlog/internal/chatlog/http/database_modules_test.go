package http

import (
	"path/filepath"
	"testing"
)

func TestBuildDatabaseBusinessModulesReportsReadyAndPartial(t *testing.T) {
	root := t.TempDir()
	dbs := map[string][]string{
		"session": {
			filepath.Join(root, "session", "session.db"),
		},
		"message": {
			filepath.Join(root, "message", "message_0.db"),
			filepath.Join(root, "message", "message_fts.db"),
			filepath.Join(root, "message", "biz_message_0.db"),
			filepath.Join(root, "message", "message_resource.db"),
		},
		"contact": {
			filepath.Join(root, "contact", "contact.db"),
			filepath.Join(root, "contact", "contact_fts.db"),
		},
		"favorite": {
			filepath.Join(root, "favorite", "favorite.db"),
		},
	}
	modules := buildDatabaseBusinessModules(dbs)
	byID := make(map[string]databaseBusinessModule, len(modules))
	for _, module := range modules {
		byID[module.ID] = module
	}
	if module := byID["conversations"]; module.Status != "ready" {
		t.Fatalf("conversations = %+v", module)
	}
	if module := byID["address_book"]; module.Status != "ready" {
		t.Fatalf("address book = %+v", module)
	}
	if module := byID["favorites"]; module.Status != "ready" {
		t.Fatalf("favorites = %+v", module)
	}
	if module := byID["media_resources"]; module.Status != "partial" {
		t.Fatalf("media resources = %+v", module)
	}
	if module := byID["social"]; module.Status != "missing" {
		t.Fatalf("social = %+v", module)
	}
}
