package handler

import (
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/env"
)

const refundAccountPurpose = "heygo-refund-bank-account-v1"
const refundAccountNamePurpose = "heygo-refund-bank-account-name-v1"

func refundEncryptionKey() []byte {
	key, _ := hex.DecodeString(env.GetString("HEYGO_ADMIN_SETTINGS_KEY", ""))
	if len(key) != 32 {
		return nil
	}
	return key
}

func normalizedAccountName(value string) string {
	words := strings.FieldsFunc(strings.ToUpper(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	sort.Strings(words)
	return strings.Join(words, " ")
}

func (a *driverAPI) supplyRefundDestination(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_refund_id", "Valid refund ID is required")
		return
	}
	var body struct {
		AccountNumber string `json:"accountNumber"`
		BankCode      string `json:"bankCode"`
	}
	if ctx.ShouldBindJSON(&body) != nil || len(body.AccountNumber) != 10 || len(body.BankCode) < 3 || len(body.BankCode) > 8 {
		driverError(ctx, http.StatusBadRequest, "invalid_bank_account", "Valid bank account number and code are required")
		return
	}
	for _, char := range body.AccountNumber + body.BankCode {
		if char < '0' || char > '9' {
			driverError(ctx, http.StatusBadRequest, "invalid_bank_account", "Bank details must be numeric")
			return
		}
	}
	key := refundEncryptionKey()
	if len(key) != 32 {
		driverError(ctx, http.StatusServiceUnavailable, "refund_encryption_unavailable", "Refund destination is unavailable")
		return
	}
	// The user chooses the account. The provider supplies the account name.
	accountName, err := verifyRefundBank(ctx.Request.Context(), body.AccountNumber, body.BankCode)
	if err != nil {
		driverError(ctx, http.StatusUnprocessableEntity, "bank_account_unverified", "Bank account could not be verified")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Destination could not be saved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var state string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT state FROM refund_requests WHERE id=$1 AND driver_id=$2::UUID FOR UPDATE`, id, driverID(ctx)).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "refund_not_found", "Refund request was not found")
		return
	}
	if err != nil || state != "draft" {
		driverError(ctx, http.StatusConflict, "refund_destination_locked", "Only a draft refund accepts a bank destination")
		return
	}
	var legalRecordID string
	var sealedName []byte
	err = tx.QueryRow(ctx.Request.Context(), `SELECT n.id::TEXT,n.name_ciphertext FROM driver_verified_legal_names n
		JOIN driver_profiles p ON p.driver_id=n.driver_id JOIN driver_documents doc ON doc.id=n.evidence_document_id
		WHERE n.driver_id=$1::UUID AND p.manual_verification_status='approved'
		AND doc.status='approved' AND (doc.expires_at IS NULL OR doc.expires_at>NOW())
		ORDER BY n.created_at DESC,n.id DESC LIMIT 1`, driverID(ctx)).Scan(&legalRecordID, &sealedName)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusConflict, "verified_legal_name_required", "Compliance identity review is required before a refund destination")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Destination could not be checked")
		return
	}
	legalName, err := openAdminValue(key, sealedName, legalNamePurpose)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Destination could not be checked")
		return
	}
	matched := normalizedAccountName(accountName) == normalizedAccountName(legalName) && normalizedAccountName(legalName) != ""
	accountCipher, err := sealAdminValue(key, body.AccountNumber, refundAccountPurpose)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Destination could not be saved")
		return
	}
	nameCipher, err := sealAdminValue(key, accountName, refundAccountNamePurpose)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Destination could not be saved")
		return
	}
	masked := "******" + body.AccountNumber[6:]
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO refund_destinations(refund_id,bank_code,account_number_ciphertext,account_number_masked,
		account_name_ciphertext,name_matched,name_record_id,verified_at)
		VALUES($1,$2,$3,$4,$5,$6,$7::UUID,NOW())
		ON CONFLICT(refund_id) DO UPDATE SET bank_code=EXCLUDED.bank_code,account_number_ciphertext=EXCLUDED.account_number_ciphertext,
		account_number_masked=EXCLUDED.account_number_masked,account_name_ciphertext=EXCLUDED.account_name_ciphertext,
		name_matched=EXCLUDED.name_matched,name_record_id=EXCLUDED.name_record_id,verified_at=NOW(),supplied_at=NOW()`, id, body.BankCode, accountCipher, masked, nameCipher, matched, legalRecordID)
	if err == nil {
		outcome := "name_mismatch"
		if matched {
			outcome = "destination_verified"
		}
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO refund_actions(id,refund_id,action,reason,before_state,after_state)
			VALUES($1,$2,$3,$4,'draft','draft')`, uuid.New(), id, outcome, "Driver-supplied bank account checked by provider; full number omitted")
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Destination could not be saved")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"refundId": id.String(), "maskedAccountNumber": masked, "bankCode": body.BankCode,
		"nameMatched": matched, "status": map[bool]string{true: "verified", false: "compliance_review_required"}[matched]}})
}

func (a *driverAPI) driverRefund(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_refund_id", "Valid refund ID is required")
		return
	}
	var state string
	var amount int64
	var masked *string
	var matched *bool
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT r.state,r.amount_kobo,d.account_number_masked,d.name_matched
		FROM refund_requests r LEFT JOIN refund_destinations d ON d.refund_id=r.id
		WHERE r.id=$1 AND r.driver_id=$2::UUID`, id, driverID(ctx)).Scan(&state, &amount, &masked, &matched)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "refund_not_found", "Refund request was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "refund_unavailable", "Refund request is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id.String(), "state": state, "amountKobo": amount,
		"maskedAccountNumber": masked, "nameMatched": matched}})
}
