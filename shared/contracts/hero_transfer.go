package contracts

import "encoding/json"

type TransferResult struct {
	Amount                   json.Number `json:"amount"`
	Reference                string      `json:"reference"`
	Currency                 string      `json:"currency"`
	Status                   string      `json:"status"`
	TransactionReference     string      `json:"transactionReference"`
	DestinationAccountNumber string      `json:"destinationAccountNumber"`
	DestinationBankCode      string      `json:"destinationBankCode"`
}
