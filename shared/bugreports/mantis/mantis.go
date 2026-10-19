// Package mantis implements the bugreports boundary using MantisBT's REST API.
package mantis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/luxipha/heyGo_backend/shared/bugreports"
)

type Config struct {
	URL, Token            string
	ProjectID, CategoryID int
	CustomFields          map[int]string
}
type Adapter struct {
	config Config
	client *http.Client
}

func New(c Config) (*Adapter, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || c.Token == "" || c.ProjectID <= 0 || c.CategoryID <= 0 {
		return nil, errors.New("invalid Mantis configuration")
	}
	if len(c.CustomFields) > 20 {
		return nil, errors.New("too many Mantis custom fields")
	}
	fields := make(map[int]string, len(c.CustomFields))
	for id, value := range c.CustomFields {
		if id <= 0 || len(value) > 255 {
			return nil, errors.New("invalid Mantis custom field")
		}
		fields[id] = value
	}
	c.CustomFields = fields
	c.URL = strings.TrimRight(c.URL, "/")
	return &Adapter{c, &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (a *Adapter) Name() string {
	// Links belong to one tracker instance and project, even after reconfiguration.
	sum := sha256.Sum256([]byte(a.config.URL + "/" + strconv.Itoa(a.config.ProjectID)))
	return fmt.Sprintf("mantis:%x", sum[:16])
}
func (a *Adapter) request(ctx context.Context, method, path string, body, out any) error {
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, a.config.URL+"/api/rest"+path, &b)
	if err != nil {
		return bugreports.ErrUnavailable
	}
	// Bare-token authentication supports the installed MantisBT 2.x releases.
	req.Header.Set("Authorization", a.config.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Identify this authorized integration; shared-hosting bot filters reject
	// the generic Go HTTP client user-agent.
	req.Header.Set("User-Agent", "HeyGo-BugReports/1.0")
	res, err := a.client.Do(req)
	if err != nil {
		return bugreports.ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// Status only: never log credentials, report content or internal issue IDs.
		log.Printf("Mantis integration request rejected: method=%s status=%d", method, res.StatusCode)
		return fmt.Errorf("bug tracker HTTP %d", res.StatusCode)
	}
	if out != nil {
		if err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(out); err != nil {
			return bugreports.ErrUnavailable
		}
	}
	return nil
}
func (a *Adapter) Create(ctx context.Context, in bugreports.Input) (string, error) {
	var out struct {
		Issue struct {
			ID int `json:"id"`
		} `json:"issue"`
	}
	body := map[string]any{"summary": in.Title, "description": in.Description, "additional_information": "HeyGo reference: " + in.Reference, "project": map[string]int{"id": a.config.ProjectID}, "category": map[string]int{"id": a.config.CategoryID}, "view_state": map[string]int{"id": 50}}
	keys := make([]int, 0, len(a.config.CustomFields))
	for id := range a.config.CustomFields {
		keys = append(keys, id)
	}
	sort.Ints(keys)
	fields := make([]map[string]any, 0, len(keys))
	for _, id := range keys {
		fields = append(fields, map[string]any{"field": map[string]int{"id": id}, "value": a.config.CustomFields[id]})
	}
	if len(fields) > 0 {
		body["custom_fields"] = fields
	}
	if err := a.request(ctx, "POST", "/issues", body, &out); err != nil {
		return "", err
	}
	if out.Issue.ID <= 0 {
		return "", bugreports.ErrUnavailable
	}
	return strconv.Itoa(out.Issue.ID), nil
}

type issue struct {
	ID          int    `json:"id"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
	Project     struct {
		ID int `json:"id"`
	} `json:"project"`
	Status struct {
		ID int `json:"id"`
	} `json:"status"`
	Notes []struct {
		Text      string `json:"text"`
		CreatedAt string `json:"created_at"`
		ViewState struct {
			ID int `json:"id"`
		} `json:"view_state"`
	} `json:"notes"`
}

func (a *Adapter) Get(ctx context.Context, id string) (bugreports.Report, error) {
	if n, e := strconv.Atoi(id); e != nil || n <= 0 {
		return bugreports.Report{}, bugreports.ErrUnavailable
	}
	var out struct {
		Issues []issue `json:"issues"`
	}
	if err := a.request(ctx, "GET", "/issues/"+id, nil, &out); err != nil {
		return bugreports.Report{}, err
	}
	if len(out.Issues) != 1 || out.Issues[0].Project.ID != a.config.ProjectID {
		return bugreports.Report{}, bugreports.ErrUnavailable
	}
	i := out.Issues[0]
	r := bugreports.Report{ID: id, Title: i.Summary, Description: i.Description, Status: status(i.Status.ID), Comments: []bugreports.Comment{}}
	for _, n := range i.Notes {
		if n.ViewState.ID == 10 {
			r.Comments = append(r.Comments, bugreports.Comment{Body: n.Text, CreatedAt: n.CreatedAt})
		}
	}
	return r, nil
}
func status(id int) string {
	switch id {
	case 80:
		return "resolved"
	case 90:
		return "closed"
	case 50:
		return "in_progress"
	case 20:
		return "waiting_on_user"
	default:
		return "open"
	}
}
func (a *Adapter) Comment(ctx context.Context, id, body string) error {
	return a.request(ctx, "POST", "/issues/"+url.PathEscape(id)+"/notes", map[string]any{"text": body, "view_state": map[string]int{"id": 10}}, nil)
}
func (a *Adapter) SetStatus(ctx context.Context, id, s string) error {
	values := map[string]int{"open": 10, "in_progress": 50, "waiting_on_user": 20, "resolved": 80, "closed": 90}
	v, ok := values[s]
	if !ok {
		return errors.New("invalid status")
	}
	return a.request(ctx, "PATCH", "/issues/"+url.PathEscape(id), map[string]any{"status": map[string]int{"id": v}}, nil)
}
