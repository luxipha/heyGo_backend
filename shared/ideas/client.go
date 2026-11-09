package ideas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Client struct {
	Base, Token, Board string
	HTTP               *http.Client
}

func Configured() (*Client, error) {
	base, token := strings.TrimRight(os.Getenv("HEYGO_IDEAS_URL"), "/"), os.Getenv("HEYGO_IDEAS_TOKEN")
	u, e := url.Parse(base)
	if os.Getenv("HEYGO_IDEAS_ENABLED") != "true" || e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || token == "" {
		return nil, errors.New("ideas integration unavailable")
	}
	return &Client{Base: base, Token: token, Board: os.Getenv("HEYGO_IDEAS_BOARD"), HTTP: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Post(ctx context.Context, path string, body any, out any) error {
	raw, e := json.Marshal(body)
	if e != nil {
		return e
	}
	u, e := url.Parse(c.Base + path)
	if e != nil {
		return e
	}
	q := u.Query()
	if c.Board != "" {
		q.Set("board", c.Board)
	}
	u.RawQuery = q.Encode()
	req, e := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(raw))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	res, e := c.HTTP.Do(req)
	if e != nil {
		return errors.New("ideas service unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return errors.New("ideas service rejected request")
	}
	return json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(out)
}
