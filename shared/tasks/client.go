package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	cloudtasks "google.golang.org/api/cloudtasks/v2"
	"google.golang.org/api/googleapi"
)

type Config struct {
	ProjectID          string
	Location           string
	Queue              string
	OIDCServiceAccount string
}

type createTaskFunc func(context.Context, string, *cloudtasks.Task) error

type Client struct {
	parent             string
	oidcServiceAccount string
	create             createTaskFunc
}

func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	service, err := cloudtasks.NewService(ctx)
	if err != nil {
		return nil, fmt.Errorf("create Cloud Tasks client: %w", err)
	}
	return newClient(cfg, func(callCtx context.Context, parent string, task *cloudtasks.Task) error {
		_, err := service.Projects.Locations.Queues.Tasks.Create(parent, &cloudtasks.CreateTaskRequest{Task: task}).Context(callCtx).Do()
		return err
	}), nil
}

func newClient(cfg Config, create createTaskFunc) *Client {
	return &Client{
		parent:             fmt.Sprintf("projects/%s/locations/%s/queues/%s", cfg.ProjectID, cfg.Location, cfg.Queue),
		oidcServiceAccount: cfg.OIDCServiceAccount,
		create:             create,
	}
}

func (cfg Config) validate() error {
	if strings.TrimSpace(cfg.ProjectID) == "" || strings.TrimSpace(cfg.Location) == "" || strings.TrimSpace(cfg.Queue) == "" {
		return fmt.Errorf("Cloud Tasks project, location, and queue are required")
	}
	if strings.TrimSpace(cfg.OIDCServiceAccount) == "" {
		return fmt.Errorf("Cloud Tasks OIDC service account is required")
	}
	return nil
}

func (c *Client) ScheduleJSON(ctx context.Context, key, targetOrigin, targetPath string, at time.Time, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Cloud Task payload: %w", err)
	}
	hash := sha256.Sum256([]byte(key))
	taskID := "task-" + hex.EncodeToString(hash[:16])
	parsed, err := url.Parse(strings.TrimSuffix(targetOrigin, "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("Cloud Tasks target must be an HTTPS origin")
	}
	targetOrigin = strings.TrimSuffix(targetOrigin, "/")
	targetURL := targetOrigin + path.Clean("/"+targetPath)
	task := &cloudtasks.Task{
		Name:             c.parent + "/tasks/" + taskID,
		ScheduleTime:     at.UTC().Format(time.RFC3339Nano),
		DispatchDeadline: "30s",
		HttpRequest: &cloudtasks.HttpRequest{
			Body:       base64.StdEncoding.EncodeToString(body),
			Headers:    map[string]string{"Content-Type": "application/json"},
			HttpMethod: "POST",
			Url:        targetURL,
			OidcToken: &cloudtasks.OidcToken{
				Audience:            targetOrigin,
				ServiceAccountEmail: c.oidcServiceAccount,
			},
		},
	}
	if err := c.create(ctx, c.parent, task); err != nil {
		var apiErr *googleapi.Error
		if errors.As(err, &apiErr) && apiErr.Code == 409 {
			return nil
		}
		return fmt.Errorf("create Cloud Task: %w", err)
	}
	return nil
}
