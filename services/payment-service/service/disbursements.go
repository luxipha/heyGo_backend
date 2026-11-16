package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type TransferResult struct {
	Amount                   json.Number `json:"amount"`
	Reference                string      `json:"reference"`
	Currency                 string      `json:"currency"`
	Status                   string      `json:"status"`
	TransactionReference     string      `json:"transactionReference"`
	DestinationAccountNumber string      `json:"destinationAccountNumber"`
	DestinationBankCode      string      `json:"destinationBankCode"`
}

func (m *monnifyClient) transferRequest(ctx context.Context, method, path string, payload any) (*TransferResult, error) {
	token, err := m.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var result struct {
		RequestSuccessful bool           `json:"requestSuccessful"`
		Body              TransferResult `json:"responseBody"`
	}
	if err = m.doJSON(ctx, method, path, "Bearer "+token, payload, &result); err != nil {
		return nil, err
	}
	if !result.RequestSuccessful || result.Body.Reference == "" {
		return nil, fmt.Errorf("transfer response unavailable; reconcile by reference")
	}
	return &result.Body, nil
}
func (m *monnifyClient) InitiateTransfer(ctx context.Context, ref string, amount int64, account, bank, source string) (*TransferResult, error) {
	if ref == "" || amount <= 0 || len(account) != 10 || bank == "" || len(source) != 10 {
		return nil, fmt.Errorf("transfer configuration is incomplete")
	}
	return m.transferRequest(ctx, http.MethodPost, "/api/v2/disbursements/single", map[string]any{"amount": json.Number(fmt.Sprintf("%d.%02d", amount/100, amount%100)), "reference": ref, "narration": "HeyGo Hero referral reward", "destinationBankCode": bank, "destinationAccountNumber": account, "currency": "NGN", "sourceAccountNumber": source, "async": true})
}
func (m *monnifyClient) TransferStatus(ctx context.Context, ref string) (*TransferResult, error) {
	return m.transferRequest(ctx, http.MethodGet, "/api/v2/disbursements/single/summary?reference="+url.QueryEscape(ref), nil)
}
func (m *monnifyClient) AuthorizeTransfer(ctx context.Context, ref, otp string) (*TransferResult, error) {
	if len(otp) < 4 || len(otp) > 10 || strings.Trim(otp, "0123456789") != "" {
		return nil, fmt.Errorf("invalid authorization code")
	}
	return m.transferRequest(ctx, http.MethodPost, "/api/v2/disbursements/single/validate-otp", map[string]string{"reference": ref, "authorizationCode": otp})
}
func (m *monnifyClient) Banks(ctx context.Context) ([]map[string]string, error) {
	token, err := m.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var result struct {
		RequestSuccessful bool                `json:"requestSuccessful"`
		Body              []map[string]string `json:"responseBody"`
	}
	err = m.doJSON(ctx, http.MethodGet, "/api/v1/banks", "Bearer "+token, nil, &result)
	if err != nil {
		return nil, err
	}
	if !result.RequestSuccessful {
		return nil, fmt.Errorf("banks unavailable")
	}
	return result.Body, nil
}

func (m *monnifyClient) ResendTransferOTP(ctx context.Context, ref string) error {
	token, err := m.accessToken(ctx)
	if err != nil {
		return err
	}
	var result struct {
		Successful bool `json:"requestSuccessful"`
	}
	if err = m.doJSON(ctx, http.MethodPost, "/api/v2/disbursements/single/resend-otp", "Bearer "+token, map[string]string{"reference": ref}, &result); err != nil {
		return err
	}
	if !result.Successful {
		return fmt.Errorf("authorization code resend unconfirmed")
	}
	return nil
}
