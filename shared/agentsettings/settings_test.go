package agentsettings

import "testing"

func TestLimits(t *testing.T) {
	s := Default()
	if s.Validate() != nil {
		t.Fatal("invalid default")
	}
	for _, bad := range []Settings{{Provider: "other", Model: "test", TimeoutMS: 3000, DailyMaxCalls: 10}, {Provider: "gemini", Model: "gemini-model", TimeoutMS: 3001, DailyMaxCalls: 10}, {Provider: "qwen", Model: "qwen-flash", TimeoutMS: 3000, DailyMaxCalls: 0}} {
		if bad.Validate() == nil {
			t.Fatal("invalid controls accepted")
		}
	}
}
