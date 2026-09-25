package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCasperIDExchangeKeepsSecretServerSide(t *testing.T) {
	var receivedSecret string
	casperID := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSecret = r.Header.Get("X-API-Secret")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"business.jwt","token_type":"Bearer"}`)
	}))
	defer casperID.Close()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/auth/casperid/exchange", casperIDExchangeHandler(CasperIDOAuthConfig{
		AppID: "heygo", APISecret: "backend-only", TokenURL: casperID.URL,
		DriverRedirectURI: "com.heygo.driver://oauth/callback",
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/casperid/exchange", strings.NewReader(
		`{"code":"code","codeVerifier":"verifier","redirectUri":"com.heygo.driver://oauth/callback"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || receivedSecret != "backend-only" {
		t.Fatalf("unexpected exchange result: status=%d secret=%q body=%s", recorder.Code, receivedSecret, recorder.Body.String())
	}
	var response struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Data.AccessToken != "business.jwt" {
		t.Fatalf("exchange response is not enveloped: %s, %v", recorder.Body.String(), err)
	}
}

func TestCasperIDExchangeRejectsUnregisteredRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/auth/casperid/exchange", casperIDExchangeHandler(CasperIDOAuthConfig{
		AppID: "heygo", APISecret: "secret", TokenURL: "https://unused.example",
		DriverRedirectURI: "com.heygo.driver://oauth/callback",
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/casperid/exchange", strings.NewReader(
		`{"code":"code","codeVerifier":"verifier","redirectUri":"attacker://callback"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
}
