package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/gin-gonic/gin"
)

type CasperIDOAuthConfig struct {
	AppID             string
	APISecret         string
	TokenURL          string
	DriverRedirectURI string
	Client            *http.Client
}

func casperIDExchangeHandler(config CasperIDOAuthConfig) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if config.AppID == "" || config.APISecret == "" || config.TokenURL == "" || config.DriverRedirectURI == "" {
			driverError(ctx, http.StatusServiceUnavailable, "oauth_unavailable", "CasperID OAuth exchange is not configured")
			return
		}
		var body struct {
			Code         string `json:"code" binding:"required"`
			CodeVerifier string `json:"codeVerifier" binding:"required"`
			RedirectURI  string `json:"redirectUri" binding:"required"`
		}
		if err := ctx.ShouldBindJSON(&body); err != nil || body.RedirectURI != config.DriverRedirectURI {
			driverError(ctx, http.StatusBadRequest, "invalid_oauth_request", "Invalid OAuth exchange request")
			return
		}
		payload, _ := json.Marshal(gin.H{
			"grant_type": "authorization_code", "code": body.Code,
			"client_id": config.AppID, "redirect_uri": config.DriverRedirectURI,
			"code_verifier": body.CodeVerifier,
		})
		req, err := http.NewRequestWithContext(ctx.Request.Context(), http.MethodPost, config.TokenURL, bytes.NewReader(payload))
		if err != nil {
			driverError(ctx, http.StatusInternalServerError, "oauth_request_failed", "Failed to create OAuth exchange")
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Secret", config.APISecret)
		client := config.Client
		if client == nil {
			client = &http.Client{Timeout: 10 * time.Second}
		}
		response, err := client.Do(req)
		if err != nil {
			driverError(ctx, http.StatusBadGateway, "oauth_exchange_failed", "CasperID token exchange failed")
			return
		}
		defer response.Body.Close()
		responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			driverError(ctx, http.StatusBadGateway, "invalid_oauth_response", "Invalid CasperID response")
			return
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			driverError(ctx, http.StatusBadGateway, "oauth_exchange_failed", "CasperID token exchange failed")
			return
		}
		var tokenResponse map[string]json.RawMessage
		if err := json.Unmarshal(responseBody, &tokenResponse); err != nil {
			driverError(ctx, http.StatusBadGateway, "invalid_oauth_response", "Invalid CasperID response")
			return
		}
		var accessToken string
		if err := json.Unmarshal(tokenResponse["access_token"], &accessToken); err != nil || accessToken == "" {
			driverError(ctx, http.StatusBadGateway, "invalid_oauth_response", "Invalid CasperID response")
			return
		}
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: tokenResponse})
	}
}
