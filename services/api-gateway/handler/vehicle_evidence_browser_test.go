package handler

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/storage"
	"io"
	"net/http"
	"time"
)

// Used only by the opt-in, loopback/database-guarded catalogue browser harness.
type catalogueQATicket struct {
	key, method, contentType string
	size                     int64
	expires                  time.Time
}
type catalogueQAObjectStore struct {
	*privacyMemoryStore
	types   map[string]string
	tickets map[string]catalogueQATicket
}

func newCatalogueQAStore() *catalogueQAObjectStore {
	return &catalogueQAObjectStore{newPrivacyMemoryStore(), map[string]string{}, map[string]catalogueQATicket{}}
}
func (s *catalogueQAObjectStore) ticket(key, method, contentType string, size int64, ttl time.Duration) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := uuid.NewString()
	s.tickets[token] = catalogueQATicket{key, method, contentType, size, time.Now().Add(ttl)}
	return "http://localhost:18081/_qa/objects/" + token
}
func (s *catalogueQAObjectStore) PresignPut(_ context.Context, key, contentType string, size int64, ttl time.Duration) (string, http.Header, error) {
	return s.ticket(key, "PUT", contentType, size, ttl), http.Header{"Content-Type": []string{contentType}}, nil
}
func (s *catalogueQAObjectStore) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	return s.ticket(key, "GET", "", 0, ttl), nil
}
func (s *catalogueQAObjectStore) Head(_ context.Context, key string) (storage.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return storage.ObjectInfo{}, fmt.Errorf("missing object")
	}
	return storage.ObjectInfo{Size: int64(len(data)), ContentType: s.types[key]}, nil
}
func (s *catalogueQAObjectStore) Put(ctx context.Context, key, contentType string, r io.Reader, size int64) error {
	if err := s.privacyMemoryStore.Put(ctx, key, contentType, r, size); err != nil {
		return err
	}
	s.mu.Lock()
	s.types[key] = contentType
	s.mu.Unlock()
	return nil
}
func (s *catalogueQAObjectStore) serve(c *gin.Context) {
	s.mu.Lock()
	ticket, ok := s.tickets[c.Param("ticket")]
	s.mu.Unlock()
	if !ok || !ticket.expires.After(time.Now()) || ticket.method != c.Request.Method {
		c.Status(403)
		return
	}
	if ticket.method == "PUT" {
		data, err := io.ReadAll(io.LimitReader(c.Request.Body, ticket.size+1))
		if err != nil || int64(len(data)) != ticket.size || ticket.contentType != c.GetHeader("Content-Type") {
			c.Status(400)
			return
		}
		s.mu.Lock()
		s.objects[ticket.key] = data
		s.types[ticket.key] = ticket.contentType
		s.mu.Unlock()
		c.Status(200)
		return
	}
	s.mu.Lock()
	data, ok := s.objects[ticket.key]
	contentType := s.types[ticket.key]
	s.mu.Unlock()
	if !ok {
		c.Status(404)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(200, contentType, data)
}
