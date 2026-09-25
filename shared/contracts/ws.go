package contracts

import (
	"encoding/json"
	"time"
)

type WSMessage struct {
	ID         string      `json:"id,omitempty"`
	Type       string      `json:"type"`
	OccurredAt time.Time   `json:"occurredAt,omitempty"`
	Data       interface{} `json:"data"`
}

type WSDriverMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}
