package chatlog

import (
	"net/url"
	"strings"
	"testing"

	chathttp "github.com/sjzar/chatlog/internal/chatlog/http"
)

func TestValidateEndpointInputRejectsUnknownAndMissingParameters(t *testing.T) {
	endpoint, _ := chathttp.FindEndpoint("history")
	if err := validateEndpointInput(endpoint, nil, url.Values{"chat": {"filehelper"}, "wat": {"1"}}, nil, "", false); err == nil || !strings.Contains(err.Error(), "unknown query parameter") {
		t.Fatalf("unexpected unknown parameter error: %v", err)
	}
	if err := validateEndpointInput(endpoint, nil, url.Values{}, nil, "", false); err == nil || !strings.Contains(err.Error(), "missing required --param chat") {
		t.Fatalf("unexpected required parameter error: %v", err)
	}
}

func TestValidateEndpointInputChecksTypeEnumAndRange(t *testing.T) {
	sessions, _ := chathttp.FindEndpoint("sessions")
	if err := validateEndpointInput(sessions, nil, url.Values{"limit": {"501"}}, nil, "", false); err == nil || !strings.Contains(err.Error(), "<= 500") {
		t.Fatalf("unexpected range error: %v", err)
	}
	databaseSearch, _ := chathttp.FindEndpoint("database_search")
	if err := validateEndpointInput(databaseSearch, nil, url.Values{"keyword": {"x"}, "mode": {"slow"}}, nil, "", false); err == nil || !strings.Contains(err.Error(), "quick, deep") {
		t.Fatalf("unexpected enum error: %v", err)
	}
}

func TestValidateEndpointInputChecksStructuredBody(t *testing.T) {
	endpoint, _ := chathttp.FindEndpoint("hook_config_set")
	if err := validateEndpointInput(endpoint, nil, nil, nil, "", false); err == nil || !strings.Contains(err.Error(), "requires --body") {
		t.Fatalf("unexpected missing body error: %v", err)
	}
	if err := validateEndpointInput(endpoint, nil, nil, []byte(`{"notify_mode":"invalid"}`), "", false); err == nil || !strings.Contains(err.Error(), "body.notify_mode") {
		t.Fatalf("unexpected body enum error: %v", err)
	}
	if err := validateEndpointInput(endpoint, nil, nil, []byte(`{"notify_mode":"post","unknown":1}`), "", false); err == nil || !strings.Contains(err.Error(), "unknown field body.unknown") {
		t.Fatalf("unexpected body field error: %v", err)
	}
	if err := validateEndpointInput(endpoint, nil, nil, []byte(`{"notify_mode":"post","before_count":0}`), "", false); err != nil {
		t.Fatalf("valid body rejected: %v", err)
	}
	qq, _ := chathttp.FindEndpoint("hook_hermes_qq_set")
	validQQ := []byte(`{"hermes_home":"/tmp/hermes","app_id":"demo","client_secret":"secret","home_channel":"group:12345678","home_channel_name":"测试群"}`)
	if err := validateEndpointInput(qq, nil, nil, validQQ, "", false); err != nil {
		t.Fatalf("valid Hermes QQ body rejected: %v", err)
	}
	obsoleteQQ := []byte(`{"app_id":"demo","bot_qq":"123","home_channel_id":"channel"}`)
	if err := validateEndpointInput(qq, nil, nil, obsoleteQQ, "", false); err == nil || !strings.Contains(err.Error(), "unknown field body.") {
		t.Fatalf("obsolete Hermes QQ body accepted: %v", err)
	}
}

func TestEndpointOutputRequirements(t *testing.T) {
	imageEndpoint, _ := chathttp.FindEndpoint("image")
	if !endpointRequiresOutput(imageEndpoint, nil) {
		t.Fatal("image bytes should require output")
	}
	if endpointRequiresOutput(imageEndpoint, url.Values{"info": {"1"}}) {
		t.Fatal("image metadata should remain JSON")
	}
	databaseData, _ := chathttp.FindEndpoint("database_data")
	if endpointRequiresOutput(databaseData, url.Values{"format": {"json"}}) {
		t.Fatal("JSON database result should not require output")
	}
	if !endpointRequiresOutput(databaseData, url.Values{"format": {"xlsx"}}) {
		t.Fatal("XLSX database result should require output")
	}
}

func TestValidateEndpointInputRequiresConfirmation(t *testing.T) {
	endpoint, _ := chathttp.FindEndpoint("cache_clear")
	if err := validateEndpointInput(endpoint, nil, nil, nil, "", false); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("unexpected confirmation error: %v", err)
	}
	if err := validateEndpointInput(endpoint, nil, nil, nil, "", true); err != nil {
		t.Fatalf("confirmed request rejected: %v", err)
	}
}
