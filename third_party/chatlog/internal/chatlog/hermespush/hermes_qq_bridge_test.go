package hermespush

import (
	"strings"
	"testing"
)

func TestHermesQQBridgeUsesOneShotRESTReadiness(t *testing.T) {
	required := []string{
		"class OutboundQQAdapter(QQAdapter)",
		"return bool(self._running and self._http_client is not None)",
		"adapter = OutboundQQAdapter(",
	}
	for _, fragment := range required {
		if !strings.Contains(hermesQQBridgeScript, fragment) {
			t.Fatalf("embedded QQ bridge missing %q", fragment)
		}
	}
}
