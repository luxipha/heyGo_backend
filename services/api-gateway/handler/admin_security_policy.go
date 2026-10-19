package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/luxipha/heyGo_backend/shared/adminauth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"net/http"
	"time"
)

// Read-only effective policy. Draft controls must not imply server enforcement.
func (a *adminAPI) securityPolicy(c *gin.Context) {
	c.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{
		"authenticationMethods": []string{"password"},
		"passwordMinBytes":      adminauth.MinPasswordBytes, "passwordMaxBytes": adminauth.MaxPasswordBytes,
		"sessionLifetimeSeconds": int64(adminSessionLifetime / time.Second),
		"twoFactorSupported":     false, "twoFactorRequired": false,
		"ipRestrictionsEnabled": false, "maxActiveSessions": nil, "editable": false,
	}})
}
