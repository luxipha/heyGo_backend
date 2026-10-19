// Package bugreports defines the provider-neutral bug tracker boundary.
package bugreports

import (
	"context"
	"errors"
)

var ErrUnavailable = errors.New("bug reporting unavailable")

type Input struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Reference   string `json:"-"`
}
type Comment struct {
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}
type Report struct {
	ID          string    `json:"-"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	Comments    []Comment `json:"comments"`
}

// Provider IDs, project configuration and credentials never cross the HTTP boundary.
// Implementations must omit private notes and other users' identifying information.
type Provider interface {
	Name() string
	Create(context.Context, Input) (string, error)
	Get(context.Context, string) (Report, error)
	Comment(context.Context, string, string) error
	SetStatus(context.Context, string, string) error
}
