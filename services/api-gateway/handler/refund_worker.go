package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"github.com/luxipha/heyGo_backend/shared/refunds"
)

type providerRefundResponse struct {
	Data struct {
		RefundReference      string      `json:"refundReference"`
		TransactionReference string      `json:"transactionReference"`
		RefundAmount         json.Number `json:"refundAmount"`
		RefundStatus         string      `json:"refundStatus"`
	} `json:"data"`
}

func refundAmountKobo(raw json.Number) (int64, error) {
	parts := strings.Split(raw.String(), ".")
	if len(parts) == 0 || len(parts) > 2 {
		return 0, fmt.Errorf("invalid refund amount")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 || whole > (1<<63-1)/100 {
		return 0, fmt.Errorf("invalid refund amount")
	}
	fraction := int64(0)
	if len(parts) == 2 {
		if len(parts[1]) == 0 || len(parts[1]) > 2 {
			return 0, fmt.Errorf("invalid refund amount")
		}
		fraction, err = strconv.ParseInt(parts[1]+strings.Repeat("0", 2-len(parts[1])), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid refund amount")
		}
	}
	return whole*100 + fraction, nil
}

func runClaimedRefund(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	var transactionReference, refundReference, reasonCode, bankCode string
	var sealedAccount []byte
	var amount int64
	err := pool.QueryRow(ctx, `SELECT t.provider_transaction_reference,r.refund_reference,r.amount_kobo,r.reason_code,
		d.bank_code,d.account_number_ciphertext FROM refund_requests r
		JOIN driver_operating_topups t ON t.id=r.topup_id JOIN refund_destinations d ON d.refund_id=r.id
		WHERE r.id=$1 AND r.state='provider_pending' AND d.name_matched`, id).
		Scan(&transactionReference, &refundReference, &amount, &reasonCode, &bankCode, &sealedAccount)
	if err != nil {
		_ = refunds.MarkUncertain(ctx, pool, id, "Approved refund evidence could not be loaded")
		return err
	}
	key := refundEncryptionKey()
	account, err := openAdminValue(key, sealedAccount, refundAccountPurpose)
	if err != nil {
		_ = refunds.MarkUncertain(ctx, pool, id, "Destination could not be decrypted")
		return err
	}
	var response providerRefundResponse
	err = paymentInternalRequest(ctx, "/internal/refunds/initiate", map[string]any{
		"transactionReference": transactionReference, "refundReference": refundReference, "amountKobo": amount,
		"reason": reasonCode, "accountNumber": account, "bankCode": bankCode,
	}, &response)
	if err != nil {
		_ = refunds.MarkUncertain(ctx, pool, id, "Provider initiation response unavailable; reconcile by reference")
		return err
	}
	if response.Data.RefundReference != refundReference || response.Data.TransactionReference != transactionReference {
		_ = refunds.MarkUncertain(ctx, pool, id, "Provider initiation response did not match source")
		return fmt.Errorf("provider refund reference mismatch")
	}
	returnedAmount, err := refundAmountKobo(response.Data.RefundAmount)
	if err != nil || returnedAmount != amount {
		_ = refunds.MarkUncertain(ctx, pool, id, "Provider initiation amount did not match source")
		return fmt.Errorf("provider refund amount mismatch")
	}
	switch response.Data.RefundStatus {
	case "COMPLETED", "FAILED":
		return refunds.ApplyTerminal(ctx, pool, refundReference, transactionReference, response.Data.RefundStatus, amount, "Monnify initiate response")
	case "PENDING":
		return nil
	default:
		_ = refunds.MarkUncertain(ctx, pool, id, "Provider initiation status unknown")
		return fmt.Errorf("unknown provider refund status")
	}
}

func reconcilePendingRefund(ctx context.Context, pool *pgxpool.Pool, reference, original string, amount int64) error {
	var response providerRefundResponse
	if err := paymentInternalRequest(ctx, "/internal/refunds/status", map[string]string{"refundReference": reference}, &response); err != nil {
		return err
	}
	if response.Data.RefundReference != reference || response.Data.TransactionReference != original {
		return fmt.Errorf("refund status reference mismatch")
	}
	returnedAmount, err := refundAmountKobo(response.Data.RefundAmount)
	if err != nil || returnedAmount != amount {
		return fmt.Errorf("refund status amount mismatch")
	}
	switch response.Data.RefundStatus {
	case "COMPLETED", "FAILED":
		return refunds.ApplyTerminal(ctx, pool, reference, original, response.Data.RefundStatus, amount, "Monnify status lookup")
	case "PENDING":
		return nil
	default:
		return fmt.Errorf("unknown refund status")
	}
}

func RunRefundJobsOnce(ctx context.Context, pool *pgxpool.Pool) error {
	if !env.GetBool("HEYGO_MONNIFY_REFUNDS_READY", false) {
		return nil
	}
	id, claimed, err := refunds.ClaimApproved(ctx, pool)
	if err != nil {
		return err
	}
	if claimed {
		if err := runClaimedRefund(ctx, pool, id); err != nil {
			logs.L().Warnw("Refund initiation needs reconciliation", "refundId", id.String(), "error", err)
		}
	}
	rows, err := pool.Query(ctx, `SELECT r.refund_reference,t.provider_transaction_reference,r.amount_kobo
		FROM refund_requests r JOIN driver_operating_topups t ON t.id=r.topup_id
		WHERE r.state IN ('provider_pending','needs_reconciliation') AND r.updated_at<NOW()-INTERVAL '1 minute'
		ORDER BY r.updated_at,r.id LIMIT 1`)
	if err != nil {
		return err
	}
	type pending struct {
		ref, original string
		amount        int64
	}
	items := make([]pending, 0)
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.ref, &item.original, &item.amount); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := reconcilePendingRefund(ctx, pool, item.ref, item.original, item.amount); err != nil {
			logs.L().Warnw("Refund status needs reconciliation", "refundReference", item.ref, "error", err)
		}
	}
	return nil
}

func StartRefundJobs(ctx context.Context, pool *pgxpool.Pool) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			jobCtx, cancel := context.WithTimeout(ctx, 50*time.Second)
			if err := RunRefundJobsOnce(jobCtx, pool); err != nil && ctx.Err() == nil {
				logs.L().Warnw("Refund maintenance failed", "error", err)
			}
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
