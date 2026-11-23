package handler

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/luxipha/heyGo_backend/shared/referrals"
)

func TestReferralErrorCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := map[error]string{
		referrals.ErrBadFormat:   "referral_bad_format",
		referrals.ErrWrongRole:   "referral_wrong_role",
		referrals.ErrOwnCode:     "referral_own_code",
		referrals.ErrSameAccount: "referral_same_account",
		referrals.ErrInvalid:     "referral_not_found",
		fmt.Errorf("wrapped: %w", referrals.ErrWrongRole): "referral_wrong_role",
	}
	for err, want := range cases {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		referralError(c, err)
		if w.Code != 404 || !strings.Contains(w.Body.String(), `"`+want+`"`) {
			t.Fatalf("%v: %d %s", err, w.Code, w.Body.String())
		}
	}
}
