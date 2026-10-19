package handler

import "testing"

func TestMantisCustomFieldsConfiguration(t *testing.T) {
	t.Setenv("BUG_REPORT_PROVIDER", "mantis")
	t.Setenv("MANTIS_URL", "https://issues.example.test")
	t.Setenv("MANTIS_API_TOKEN", "test")
	t.Setenv("MANTIS_RIDER_PROJECT_ID", "2")
	t.Setenv("MANTIS_RIDER_CATEGORY_ID", "2")
	for _, raw := range []string{`invalid`, `{"0":"Rider"}`, `{"not-an-id":"Rider"}`, `{"1":true}`} {
		t.Setenv("MANTIS_RIDER_CUSTOM_FIELDS_JSON", raw)
		if newBugReportAPI(nil).provider != nil {
			t.Fatalf("invalid field mapping enabled provider: %s", raw)
		}
	}
	t.Setenv("MANTIS_RIDER_CUSTOM_FIELDS_JSON", `{"1":"Rider","2":"Rider App","4":"Unknown"}`)
	if newBugReportAPI(nil).provider == nil {
		t.Fatal("valid mapping did not enable provider")
	}
	t.Setenv("BUG_REPORT_PROVIDER", "disabled")
	if newBugReportAPI(nil).provider != nil {
		t.Fatal("provider disable switch ignored")
	}
}
