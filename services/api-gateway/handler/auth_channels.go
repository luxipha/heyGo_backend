package handler

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

func (a *phoneAuthAPI) channelReady(ctx context.Context, channel string) (bool, error) {
	if channel == "phone" {
		if a.smsSender == nil {
			return false, nil
		}
		if _, configured := a.smsSender.(*configuredKudiSMSSender); configured {
			return authSMSReady(ctx, a.pool, a.deliveryKey)
		}
		return true, nil
	}
	if a.emailSender == nil {
		return false, nil
	}
	if _, configured := a.emailSender.(*configuredResendOTPSender); configured {
		return authEmailReady(ctx, a.pool, a.deliveryKey)
	}
	return true, nil
}
func (a *phoneAuthAPI) authChannels(c *gin.Context) {
	email, err := a.channelReady(c.Request.Context(), "email")
	if err != nil {
		driverError(c, 503, "auth_channels_unavailable", "Sign-in methods are temporarily unavailable")
		return
	}
	sms, err := a.channelReady(c.Request.Context(), "phone")
	if err != nil {
		driverError(c, 503, "auth_channels_unavailable", "Sign-in methods are temporarily unavailable")
		return
	}
	if len(a.pepper) < 32 {
		email = false
		sms = false
	}
	if !email && a.pool != nil && len(a.deliveryKey) == 32 {
		review, e := readReviewOTP(c.Request.Context(), a.pool)
		if e != nil {
			driverError(c, 503, "auth_channels_unavailable", "Sign-in methods are temporarily unavailable")
			return
		}
		email = review.active()
	}
	preferred := ""
	if email {
		preferred = "email"
	} else if sms {
		preferred = "phone"
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"email": gin.H{"enabled": email}, "sms": gin.H{"enabled": sms}, "preferredChannel": preferred, "scope": "new_otp_requests"}})
}
