package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/luxipha/heyGo_backend/services/payment-service/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type monnifyClient struct {
	config     *types.PaymentConfig
	httpClient *http.Client
}

func NewMonnifyClient(config *types.PaymentConfig) *monnifyClient {
	return &monnifyClient{config: config, httpClient: &http.Client{Timeout: 15 * time.Second}}
}

func (m *monnifyClient) Ping(ctx context.Context) error { _, err := m.accessToken(ctx); return err }

type VerifiedTransaction struct {
	PaymentReference     string      `json:"paymentReference"`
	TransactionReference string      `json:"transactionReference"`
	PaymentStatus        string      `json:"paymentStatus"`
	CurrencyCode         string      `json:"currencyCode"`
	AmountPaid           json.Number `json:"amountPaid"`
	TotalPayable         json.Number `json:"totalPayable"`
	CheckoutURL          string      `json:"checkoutUrl"`
	PaymentMethod        string      `json:"paymentMethod"`
}

type VerifiedBankAccount struct {
	AccountNumber string `json:"accountNumber"`
	AccountName   string `json:"accountName"`
	BankCode      string `json:"bankCode"`
	BankName      string `json:"bankName"`
}

func (m *monnifyClient) VerifyBankAccount(ctx context.Context, accountNumber, bankCode string) (*VerifiedBankAccount, error) {
	if len(accountNumber) != 10 || len(bankCode) < 3 || len(bankCode) > 8 {
		return nil, fmt.Errorf("valid bank account number and bank code are required")
	}
	for _, digit := range accountNumber + bankCode {
		if digit < '0' || digit > '9' {
			return nil, fmt.Errorf("bank account number and bank code must be numeric")
		}
	}
	token, err := m.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var response struct {
		RequestSuccessful bool                `json:"requestSuccessful"`
		ResponseBody      VerifiedBankAccount `json:"responseBody"`
	}
	path := "/api/v2/disbursements/account/validate?accountNumber=" + url.QueryEscape(accountNumber) + "&bankCode=" + url.QueryEscape(bankCode)
	if err := m.doJSON(ctx, http.MethodGet, path, "Bearer "+token, nil, &response); err != nil {
		return nil, fmt.Errorf("verify bank account: %w", err)
	}
	if !response.RequestSuccessful || response.ResponseBody.AccountNumber != accountNumber || response.ResponseBody.BankCode != bankCode || strings.TrimSpace(response.ResponseBody.AccountName) == "" {
		return nil, fmt.Errorf("bank account could not be verified")
	}
	return &response.ResponseBody, nil
}

type RefundResult struct {
	RefundReference      string      `json:"refundReference"`
	TransactionReference string      `json:"transactionReference"`
	RefundAmount         json.Number `json:"refundAmount"`
	RefundStatus         string      `json:"refundStatus"`
}

func (m *monnifyClient) InitiateRefund(ctx context.Context, transactionReference, refundReference string, amountKobo int64, reason, accountNumber, bankCode string) (*RefundResult, error) {
	if transactionReference == "" || refundReference == "" || amountKobo < 10000 || len(reason) < 1 || len(reason) > 64 || len(accountNumber) != 10 || bankCode == "" {
		return nil, fmt.Errorf("valid refund reference, amount, reason, and bank destination are required")
	}
	token, err := m.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"transactionReference":       transactionReference,
		"refundReference":            refundReference,
		"refundAmount":               json.Number(fmt.Sprintf("%d.%02d", amountKobo/100, amountKobo%100)),
		"refundReason":               reason,
		"customerNote":               "HeyGo refund",
		"destinationAccountNumber":   accountNumber,
		"destinationAccountBankCode": bankCode,
	}
	var response struct {
		RequestSuccessful bool         `json:"requestSuccessful"`
		ResponseBody      RefundResult `json:"responseBody"`
	}
	if err := m.doJSON(ctx, http.MethodPost, "/api/v1/refunds/initiate-refund", "Bearer "+token, payload, &response); err != nil {
		return nil, fmt.Errorf("initiate refund: %w", err)
	}
	if !response.RequestSuccessful || response.ResponseBody.RefundReference != refundReference || response.ResponseBody.TransactionReference != transactionReference {
		return nil, fmt.Errorf("refund response did not match the request")
	}
	return &response.ResponseBody, nil
}

func (m *monnifyClient) RefundStatus(ctx context.Context, refundReference string) (*RefundResult, error) {
	if refundReference == "" {
		return nil, fmt.Errorf("refund reference is required")
	}
	token, err := m.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var response struct {
		RequestSuccessful bool         `json:"requestSuccessful"`
		ResponseBody      RefundResult `json:"responseBody"`
	}
	if err := m.doJSON(ctx, http.MethodGet, "/api/v1/refunds/"+url.PathEscape(refundReference), "Bearer "+token, nil, &response); err != nil {
		return nil, fmt.Errorf("get refund status: %w", err)
	}
	if !response.RequestSuccessful || response.ResponseBody.RefundReference != refundReference {
		return nil, fmt.Errorf("refund status response did not match the request")
	}
	return &response.ResponseBody, nil
}

func (m *monnifyClient) VerifyTransaction(ctx context.Context, transactionReference string) (*VerifiedTransaction, error) {
	if transactionReference == "" {
		return nil, fmt.Errorf("transaction reference is required")
	}
	verified, err := m.queryTransaction(ctx, "transactionReference", transactionReference)
	if err != nil {
		return nil, err
	}
	if verified.TransactionReference != transactionReference {
		return nil, fmt.Errorf("verify Monnify transaction: transaction reference mismatch")
	}
	return verified, nil
}

func (m *monnifyClient) queryTransaction(ctx context.Context, field, reference string) (*VerifiedTransaction, error) {
	token, err := m.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var response struct {
		RequestSuccessful bool                `json:"requestSuccessful"`
		ResponseMessage   string              `json:"responseMessage"`
		ResponseBody      VerifiedTransaction `json:"responseBody"`
	}
	path := "/api/v2/merchant/transactions/query?" + field + "=" + url.QueryEscape(reference)
	if err := m.doJSON(ctx, http.MethodGet, path, "Bearer "+token, nil, &response); err != nil {
		return nil, fmt.Errorf("verify Monnify transaction: %w", err)
	}
	if !response.RequestSuccessful || response.ResponseBody.TransactionReference == "" {
		return nil, fmt.Errorf("verify Monnify transaction: %s", response.ResponseMessage)
	}
	return &response.ResponseBody, nil
}

func (m *monnifyClient) CreatePaymentSession(ctx context.Context, paymentReference string, amount int64, currency string, metadata map[string]string) (*types.ProviderSession, error) {
	if strings.TrimSpace(paymentReference) == "" {
		return nil, fmt.Errorf("payment reference is required")
	}
	customerEmail := metadata["riderID"]
	if !strings.Contains(customerEmail, "@") {
		customerEmail += "@rider.heygo.invalid"
	}
	return m.initializeTransaction(ctx, amount, currency, paymentReference, customerEmail, "HeyGo rider", "HeyGo ride "+metadata["tripID"], metadata)
}

// RecoverPaymentSession resolves the provider transaction created by an
// initialization call whose response was lost before it could be persisted.
func (m *monnifyClient) RecoverPaymentSession(ctx context.Context, paymentReference string, amount int64, currency string) (*types.ProviderSession, error) {
	verified, err := m.queryTransaction(ctx, "paymentReference", paymentReference)
	if err != nil {
		return nil, err
	}
	if verified.PaymentReference != paymentReference {
		return nil, fmt.Errorf("recover Monnify transaction: payment reference mismatch")
	}
	payable, err := strconv.ParseFloat(verified.TotalPayable.String(), 64)
	if err != nil || int64(math.Round(payable*100)) != amount || !strings.EqualFold(verified.CurrencyCode, currency) {
		return nil, fmt.Errorf("recover Monnify transaction: amount or currency mismatch")
	}
	checkoutURL := strings.TrimSpace(verified.CheckoutURL)
	if checkoutURL == "" {
		checkoutBase := "https://sdk.monnify.com/checkout/"
		if strings.Contains(strings.ToLower(m.config.BaseURL), "sandbox") {
			checkoutBase = "https://sandbox.sdk.monnify.com/checkout/"
		}
		checkoutURL = checkoutBase + url.PathEscape(verified.TransactionReference)
	}
	return &types.ProviderSession{
		PaymentReference:     paymentReference,
		TransactionReference: verified.TransactionReference,
		CheckoutURL:          checkoutURL,
	}, nil
}

func (m *monnifyClient) CreateOperatingTopupSession(ctx context.Context, amountKobo int64, paymentReference, driverEmail, driverID string) (*types.ProviderSession, error) {
	if amountKobo <= 0 || paymentReference == "" || driverEmail == "" || driverID == "" {
		return nil, fmt.Errorf("valid top-up amount, reference, driver, and account email are required")
	}
	return m.initializeTransaction(ctx, amountKobo, "NGN", paymentReference, driverEmail, "HeyGo driver", "HeyGo Operating Balance top-up", map[string]string{
		"purpose": "operating_balance_topup", "driverID": driverID,
	})
}

func (m *monnifyClient) CreateNoShowDebtPaymentSession(ctx context.Context, amountKobo int64, paymentReference, riderEmail, riderID, debtID string) (*types.ProviderSession, error) {
	if amountKobo <= 0 || paymentReference == "" || riderEmail == "" || riderID == "" || debtID == "" {
		return nil, fmt.Errorf("valid debt amount, reference, rider email, rider, and debt are required")
	}
	return m.initializeTransaction(ctx, amountKobo, "NGN", paymentReference, riderEmail, "HeyGo rider", "HeyGo approved no-show fee", map[string]string{
		"purpose": "rider_no_show_debt", "riderID": riderID, "debtID": debtID,
	})
}

func (m *monnifyClient) initializeTransaction(ctx context.Context, amount int64, currency, paymentReference, customerEmail, customerName, description string, metadata map[string]string) (*types.ProviderSession, error) {
	token, err := m.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"amount":             json.Number(fmt.Sprintf("%d.%02d", amount/100, amount%100)),
		"customerEmail":      customerEmail,
		"customerName":       customerName,
		"paymentReference":   paymentReference,
		"paymentDescription": description,
		"currencyCode":       strings.ToUpper(currency),
		"contractCode":       m.config.ContractCode,
		"redirectUrl":        m.config.RedirectURL,
		"metadata":           metadata,
	}

	var response struct {
		RequestSuccessful bool   `json:"requestSuccessful"`
		ResponseMessage   string `json:"responseMessage"`
		ResponseBody      struct {
			PaymentReference     string `json:"paymentReference"`
			TransactionReference string `json:"transactionReference"`
			CheckoutURL          string `json:"checkoutUrl"`
		} `json:"responseBody"`
	}
	if err := m.doJSON(ctx, http.MethodPost, "/api/v1/merchant/transactions/init-transaction", "Bearer "+token, payload, &response); err != nil {
		return nil, fmt.Errorf("initialize Monnify transaction: %w", err)
	}
	if !response.RequestSuccessful || response.ResponseBody.TransactionReference == "" || response.ResponseBody.CheckoutURL == "" {
		return nil, fmt.Errorf("initialize Monnify transaction: %s", response.ResponseMessage)
	}
	if response.ResponseBody.PaymentReference != paymentReference {
		return nil, fmt.Errorf("initialize Monnify transaction: payment reference mismatch")
	}

	return &types.ProviderSession{
		PaymentReference: paymentReference, TransactionReference: response.ResponseBody.TransactionReference,
		CheckoutURL: response.ResponseBody.CheckoutURL,
	}, nil
}

func (m *monnifyClient) accessToken(ctx context.Context) (string, error) {
	credentials := base64.StdEncoding.EncodeToString([]byte(m.config.APIKey + ":" + m.config.SecretKey))
	var response struct {
		RequestSuccessful bool   `json:"requestSuccessful"`
		ResponseMessage   string `json:"responseMessage"`
		ResponseBody      struct {
			AccessToken string `json:"accessToken"`
		} `json:"responseBody"`
	}
	if err := m.doJSON(ctx, http.MethodPost, "/api/v1/auth/login", "Basic "+credentials, nil, &response); err != nil {
		return "", fmt.Errorf("authenticate with Monnify: %w", err)
	}
	if !response.RequestSuccessful || response.ResponseBody.AccessToken == "" {
		return "", fmt.Errorf("authenticate with Monnify: %s", response.ResponseMessage)
	}
	return response.ResponseBody.AccessToken, nil
}

func (m *monnifyClient) doJSON(ctx context.Context, method, path, authorization string, payload any, output any) error {
	pathLabel := strings.SplitN(path, "?", 2)[0]
	ctx, span := otel.Tracer("moniepoint-monnify").Start(ctx, "monnify "+method+" "+pathLabel)
	defer span.End()
	span.SetAttributes(attribute.String("http.request.method", method), attribute.String("server.address", m.config.BaseURL))
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(m.config.BaseURL, "/")+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", "application/json")
	res, err := m.httpClient.Do(req)
	if err != nil {
		// The URL may contain a bank account number. Do not surface it in
		// traces, logs, or errors returned to callers.
		span.SetStatus(codes.Error, "provider request failed")
		return fmt.Errorf("provider request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		span.SetAttributes(attribute.Int("http.response.status_code", res.StatusCode))
		span.SetStatus(codes.Error, "provider error")
		return fmt.Errorf("provider returned HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(output)
}
