package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKudiOTPContractAndAcceptance(t *testing.T) {
	for _, body := range []string{`{"status":"success","error_code":"000"}`, `{"status":"error","error_code":"100"}`, `{"status":"success","error_code":"100"}`, `{}`, `not json`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("Authorization") != "" {
					t.Error("incorrect authentication/method")
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					return
				}
				for k, v := range map[string]string{"token": "synthetic-key", "senderID": "HeyGo", "recipients": "2348012345678", "otp": "123456", "appnamecode": "app", "templatecode": "template"} {
					if r.FormValue(k) != v {
						t.Errorf("incorrect field %s", k)
					}
				}
				if r.FormValue("otp_duration") != "" {
					t.Error("provider-generated OTP lifecycle used")
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			err := sendKudiOTP(context.Background(), server.URL, server.Client(), smsSettings{SenderID: "HeyGo", AppNameCode: "app", TemplateCode: "template"}, "synthetic-key", "+2348012345678", "123456")
			if (err == nil) != (body == `{"status":"success","error_code":"000"}`) {
				t.Fatalf("acceptance: %v", err)
			}
		})
	}
}
func TestKudiRejectsForeignPhoneBeforeDelivery(t *testing.T) {
	if err := sendKudiOTP(context.Background(), "invalid", nil, smsSettings{}, "", "+18012345678", "123456"); err == nil {
		t.Fatal("foreign phone accepted")
	}
}
