package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luxipha/heyGo_backend/services/payment-service/types"
)

func TestMonnifyRefundAndBankVerification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			_ = json.NewEncoder(w).Encode(map[string]any{"requestSuccessful": true, "responseBody": map[string]any{"accessToken": "token"}})
			return
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("missing bearer token")
		}
		switch r.URL.Path {
		case "/api/v2/disbursements/account/validate":
			if r.URL.Query().Get("accountNumber") != "0123456789" || r.URL.Query().Get("bankCode") != "044" {
				t.Errorf("incorrect bank lookup")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"requestSuccessful": true, "responseBody": map[string]any{"accountNumber": "0123456789", "bankCode": "044", "accountName": "Test Driver", "bankName": "Test Bank"}})
		case "/api/v1/refunds/initiate-refund":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request["refundAmount"] != float64(100) || request["destinationAccountNumber"] != "0123456789" || request["destinationAccountBankCode"] != "044" {
				t.Errorf("incorrect refund payload: %#v", request)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"requestSuccessful": true, "responseBody": map[string]any{"transactionReference": "MNFY|1", "refundReference": "REF-1", "refundAmount": 100, "refundStatus": "PENDING"}})
		case "/api/v1/refunds/REF-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"requestSuccessful": true, "responseBody": map[string]any{"transactionReference": "MNFY|1", "refundReference": "REF-1", "refundAmount": 100, "refundStatus": "COMPLETED"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewMonnifyClient(&types.PaymentConfig{BaseURL: server.URL, APIKey: "key", SecretKey: "secret"})
	if bank, err := client.VerifyBankAccount(context.Background(), "0123456789", "044"); err != nil || bank.AccountName != "Test Driver" {
		t.Fatalf("bank=%+v err=%v", bank, err)
	}
	if refund, err := client.InitiateRefund(context.Background(), "MNFY|1", "REF-1", 10000, "Duplicate topup", "0123456789", "044"); err != nil || refund.RefundStatus != "PENDING" {
		t.Fatalf("refund=%+v err=%v", refund, err)
	}
	if refund, err := client.RefundStatus(context.Background(), "REF-1"); err != nil || refund.RefundStatus != "COMPLETED" {
		t.Fatalf("status=%+v err=%v", refund, err)
	}
}

func TestMonnifyCheckoutFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("api-key:secret-key"))
			if r.Header.Get("Authorization") != expected {
				t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"requestSuccessful": true, "responseBody": map[string]any{"accessToken": "token"},
			})
		case "/api/v1/merchant/transactions/init-transaction":
			if r.Header.Get("Authorization") != "Bearer token" {
				t.Fatalf("unexpected bearer token %q", r.Header.Get("Authorization"))
			}
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request["amount"] != float64(25) || request["currencyCode"] != "NGN" || request["contractCode"] != "contract" {
				t.Fatalf("unexpected checkout request: %#v", request)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"requestSuccessful": true,
				"responseBody": map[string]any{
					"paymentReference": request["paymentReference"], "transactionReference": "MNFY|1",
					"checkoutUrl": "https://checkout.monnify.test/1",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	processor := NewMonnifyClient(&types.PaymentConfig{
		BaseURL: server.URL, APIKey: "api-key", SecretKey: "secret-key",
		ContractCode: "contract", RedirectURL: "https://heygo.test/payment",
	})
	session, err := processor.CreatePaymentSession(context.Background(), "heygo-trip-test", 2500, "NGN", map[string]string{
		"tripID": "trip-1", "riderID": "rider-1", "driverID": "driver-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.TransactionReference != "MNFY|1" || session.CheckoutURL != "https://checkout.monnify.test/1" {
		t.Fatalf("unexpected provider session: %#v", session)
	}
}

func TestMonnifyRecoversCheckoutByPaymentReference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			_ = json.NewEncoder(w).Encode(map[string]any{"requestSuccessful": true, "responseBody": map[string]any{"accessToken": "token"}})
			return
		}
		if r.URL.Path != "/api/v2/merchant/transactions/query" || r.URL.Query().Get("paymentReference") != "heygo-trip-test" {
			t.Errorf("unexpected recovery request: %s", r.URL.String())
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"requestSuccessful": true, "responseBody": map[string]any{
			"transactionReference": "MNFY|recover|1", "paymentReference": "heygo-trip-test", "paymentStatus": "PENDING", "currencyCode": "NGN", "amountPaid": 0, "totalPayable": 25,
		}})
	}))
	defer server.Close()
	client := NewMonnifyClient(&types.PaymentConfig{BaseURL: server.URL, APIKey: "key", SecretKey: "secret"})
	session, err := client.RecoverPaymentSession(context.Background(), "heygo-trip-test", 2500, "NGN")
	if err != nil {
		t.Fatal(err)
	}
	if session.TransactionReference != "MNFY|recover|1" || session.CheckoutURL != "https://sdk.monnify.com/checkout/MNFY%7Crecover%7C1" {
		t.Fatalf("unexpected recovered provider session: %#v", session)
	}
}

func TestMonnifyVerifyTransactionUsesEncodedReference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			_ = json.NewEncoder(w).Encode(map[string]any{"requestSuccessful": true, "responseBody": map[string]any{"accessToken": "token"}})
			return
		}
		if r.URL.Path != "/api/v2/merchant/transactions/query" || r.URL.Query().Get("transactionReference") != "MNFY|1" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("unexpected verification request: %s", r.URL.String())
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"requestSuccessful": true, "responseBody": map[string]any{
			"transactionReference": "MNFY|1", "paymentReference": "heygo-ob-1", "paymentStatus": "PAID", "currencyCode": "NGN", "amountPaid": 50,
		}})
	}))
	defer server.Close()
	client := NewMonnifyClient(&types.PaymentConfig{BaseURL: server.URL, APIKey: "key", SecretKey: "secret"})
	verified, err := client.VerifyTransaction(context.Background(), "MNFY|1")
	if err != nil || verified.PaymentStatus != "PAID" || verified.AmountPaid.String() != "50" {
		t.Fatalf("unexpected verification result: %#v, %v", verified, err)
	}
}

func TestMonnifyOperatingTopupUsesAccountEmail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			_ = json.NewEncoder(w).Encode(map[string]any{"requestSuccessful": true, "responseBody": map[string]any{"accessToken": "token"}})
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		metadata, _ := request["metadata"].(map[string]any)
		if request["customerEmail"] != "driver@example.com" || request["amount"] != float64(50.25) ||
			request["paymentReference"] != "heygo-ob-test" || metadata["purpose"] != "operating_balance_topup" {
			t.Errorf("unexpected top-up checkout: %#v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"requestSuccessful": true, "responseBody": map[string]any{
			"paymentReference": "heygo-ob-test", "transactionReference": "MNFY|topup", "checkoutUrl": "https://checkout.test/topup",
		}})
	}))
	defer server.Close()
	client := NewMonnifyClient(&types.PaymentConfig{BaseURL: server.URL, APIKey: "key", SecretKey: "secret", ContractCode: "contract"})
	session, err := client.CreateOperatingTopupSession(context.Background(), 5025, "heygo-ob-test", "driver@example.com", "driver-id")
	if err != nil || session.TransactionReference != "MNFY|topup" {
		t.Fatalf("top-up session: %#v, %v", session, err)
	}
}
