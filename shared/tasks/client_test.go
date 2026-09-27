package tasks

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	cloudtasks "google.golang.org/api/cloudtasks/v2"
	"google.golang.org/api/googleapi"
)

func testConfig() Config {
	return Config{
		ProjectID:          "heygo-ng",
		Location:           "europe-west1",
		Queue:              "driver-offer-expiry",
		OIDCServiceAccount: "tasks@heygo-ng.iam.gserviceaccount.com",
	}
}

func TestScheduleJSONBuildsDeterministicOIDCTask(t *testing.T) {
	var gotParent string
	var gotTask *cloudtasks.Task
	client := newClient(testConfig(), func(_ context.Context, parent string, task *cloudtasks.Task) error {
		gotParent, gotTask = parent, task
		return nil
	})
	at := time.Date(2026, 9, 26, 14, 0, 0, 123000000, time.UTC)
	if err := client.ScheduleJSON(context.Background(), "trip-1:2", "https://driver.example.run.app", "/internal/tasks/offers/expire", at, map[string]any{"tripId": "trip-1"}); err != nil {
		t.Fatal(err)
	}
	if gotParent != "projects/heygo-ng/locations/europe-west1/queues/driver-offer-expiry" {
		t.Fatalf("parent=%q", gotParent)
	}
	if gotTask == nil || !strings.HasPrefix(gotTask.Name, gotParent+"/tasks/task-") {
		t.Fatalf("name=%q", gotTask.Name)
	}
	if gotTask.ScheduleTime != at.Format(time.RFC3339Nano) || gotTask.HttpRequest.Url != "https://driver.example.run.app/internal/tasks/offers/expire" {
		t.Fatalf("unexpected task: %#v", gotTask)
	}
	if gotTask.HttpRequest.OidcToken.ServiceAccountEmail != testConfig().OIDCServiceAccount || gotTask.HttpRequest.OidcToken.Audience != "https://driver.example.run.app" {
		t.Fatalf("unexpected OIDC config: %#v", gotTask.HttpRequest.OidcToken)
	}
	body, err := base64.StdEncoding.DecodeString(gotTask.HttpRequest.Body)
	if err != nil || string(body) != `{"tripId":"trip-1"}` {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestScheduleJSONTreatsDuplicateTaskAsSuccess(t *testing.T) {
	client := newClient(testConfig(), func(context.Context, string, *cloudtasks.Task) error {
		return &googleapi.Error{Code: 409, Message: "already exists"}
	})
	if err := client.ScheduleJSON(context.Background(), "same", "https://driver.example.run.app", "/internal/tasks/offers/expire", time.Now(), struct{}{}); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleJSONReturnsCreateFailure(t *testing.T) {
	client := newClient(testConfig(), func(context.Context, string, *cloudtasks.Task) error {
		return errors.New("unavailable")
	})
	if err := client.ScheduleJSON(context.Background(), "same", "https://driver.example.run.app", "/internal/tasks/offers/expire", time.Now(), struct{}{}); err == nil {
		t.Fatal("expected create failure")
	}
}

func TestConfigRequiresSecureTarget(t *testing.T) {
	client := newClient(testConfig(), func(context.Context, string, *cloudtasks.Task) error { return nil })
	if err := client.ScheduleJSON(context.Background(), "same", "http://driver.example.test", "/task", time.Now(), struct{}{}); err == nil {
		t.Fatal("expected HTTPS validation error")
	}
}
