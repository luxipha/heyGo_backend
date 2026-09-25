package types

import "time"

// PaymentStatus represents the current status of a payment
type PaymentStatus string

const (
	PaymentStatusPending   PaymentStatus = "pending"
	PaymentStatusSuccess   PaymentStatus = "success"
	PaymentStatusFailed    PaymentStatus = "failed"
	PaymentStatusCancelled PaymentStatus = "cancelled"
)

// Payment represents a payment transaction
type Payment struct {
	ID                   string         `json:"id"`
	TripID               string         `json:"tripID"`
	RiderID              string         `json:"riderID"`
	DriverID             string         `json:"driverID"`
	Amount               int64          `json:"amount"`
	Currency             string         `json:"currency"`
	Status               PaymentStatus  `json:"status"`
	PaymentReference     string         `json:"paymentReference"`
	TransactionReference string         `json:"transactionReference"`
	CheckoutURL          string         `json:"checkoutURL"`
	ProviderMetadata     map[string]any `json:"providerMetadata,omitempty"`
	CreatedAt            time.Time      `json:"createdAt"`
	UpdatedAt            time.Time      `json:"updatedAt"`
}

// PaymentIntent represents the intent to collect a payment
type PaymentIntent struct {
	ID                   string    `json:"id"`
	TripID               string    `json:"tripID"`
	RiderID              string    `json:"riderID"`
	DriverID             string    `json:"driverID"`
	Amount               int64     `json:"amount"`
	Currency             string    `json:"currency"`
	PaymentReference     string    `json:"paymentReference"`
	TransactionReference string    `json:"transactionReference"`
	CheckoutURL          string    `json:"checkoutURL"`
	CreatedAt            time.Time `json:"createdAt"`
}

// PaymentConfig holds the configuration for the payment service
type PaymentConfig struct {
	BaseURL      string `json:"baseURL"`
	APIKey       string `json:"apiKey"`
	SecretKey    string `json:"secretKey"`
	ContractCode string `json:"contractCode"`
	RedirectURL  string `json:"redirectURL"`
}

type ProviderSession struct {
	PaymentReference     string
	TransactionReference string
	CheckoutURL          string
}
