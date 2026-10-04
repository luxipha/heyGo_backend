package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	"github.com/luxipha/heyGo_backend/shared/env"
)

var refundProviderHTTPClient = &http.Client{Timeout: 20 * time.Second}

func paymentInternalRequest(ctx context.Context, path string, body any, result any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	base := strings.TrimRight(env.GetString("PAYMENT_SERVICE_URL", "http://payment-service:9200"), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("payment service unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Token", env.GetString("INTERNAL_SERVICE_TOKEN", ""))
	if err := sharedauth.AddCloudRunIdentity(req, env.GetString("PAYMENT_SERVICE_AUDIENCE", "")); err != nil {
		return fmt.Errorf("payment service identity unavailable")
	}
	response, err := refundProviderHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("payment service unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("payment service rejected verification")
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(result); err != nil {
		return fmt.Errorf("payment service returned invalid verification")
	}
	return nil
}

func verifyRefundSource(ctx context.Context, transactionReference string) (struct {
	Data struct {
		TransactionReference string `json:"transactionReference"`
		PaymentReference     string `json:"paymentReference"`
		AmountKobo           int64  `json:"amountKobo"`
		PaymentMethod        string `json:"paymentMethod"`
	} `json:"data"`
}, error) {
	var result struct {
		Data struct {
			TransactionReference string `json:"transactionReference"`
			PaymentReference     string `json:"paymentReference"`
			AmountKobo           int64  `json:"amountKobo"`
			PaymentMethod        string `json:"paymentMethod"`
		} `json:"data"`
	}
	err := paymentInternalRequest(ctx, "/internal/refunds/verify-source", map[string]string{"transactionReference": transactionReference}, &result)
	return result, err
}

func verifyRefundBank(ctx context.Context, accountNumber, bankCode string) (string, error) {
	var result struct {
		Data struct {
			AccountName string `json:"accountName"`
		} `json:"data"`
	}
	if err := paymentInternalRequest(ctx, "/internal/refund-bank-accounts/verify", map[string]string{"accountNumber": accountNumber, "bankCode": bankCode}, &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.Data.AccountName) == "" {
		return "", fmt.Errorf("bank account name unavailable")
	}
	return result.Data.AccountName, nil
}
