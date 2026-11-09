package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/monnifysettings"
	"strings"
	"time"
)

func (a *adminAPI) verifyMonnifyCollection(c *gin.Context) {
	id := c.Param("tripID")
	if uuid.Validate(id) != nil {
		driverError(c, 400, "invalid_trip_id", "A valid trip ID is required.")
		return
	}
	var rider string
	if a.pool.QueryRow(c.Request.Context(), `SELECT rider_id::text FROM rider_trip_payments WHERE trip_id=$1::uuid`, id).Scan(&rider) != nil {
		driverError(c, 404, "payment_not_found", "Trip collection was not found.")
		return
	}
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	if e = adminAudit(c, tx, "monnify.trip.verification.requested", "", gin.H{"tripId": id}); e == nil {
		e = tx.Commit(c.Request.Context())
	}
	if e != nil {
		heroFailure(c)
		return
	}
	var result struct {
		Data map[string]any `json:"data"`
	}
	if paymentInternalRequest(c.Request.Context(), "/internal/trip-payments/refresh", gin.H{"tripId": id, "riderId": rider}, &result) != nil {
		driverError(c, 503, "provider_verification_unavailable", "Provider verification could not be completed. Payment status was not overridden.")
		return
	}
	heroData(c, result.Data)
}

func (a *adminAPI) monnifyCollections(c *gin.Context) {
	limit, at, id, ok := staffPage(c)
	if !ok {
		return
	}
	rows, e := a.pool.Query(c.Request.Context(), `SELECT trip_id::text,p.created_at,jsonb_build_object('tripId',trip_id,'riderId',rider_id,'driverId',driver_id,'amountKobo',amount_kobo,'currency','NGN','status',p.status,'paymentReference',payment_reference,'transactionReference',transaction_reference,'environment',s.environment,'createdAt',p.created_at,'verifiedAt',verified_at) FROM rider_trip_payments p JOIN admin_monnify_settings s ON s.version=p.monnify_config_version WHERE ($1::timestamptz IS NULL OR (p.created_at,p.trip_id)<($1,$2::uuid)) ORDER BY p.created_at DESC,p.trip_id DESC LIMIT $3`, at, id, limit+1)
	if e != nil {
		heroFailure(c)
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	var next any
	hasMore := false
	for rows.Next() {
		var key string
		var created time.Time
		var raw []byte
		if rows.Scan(&key, &created, &raw) != nil {
			heroFailure(c)
			return
		}
		if len(items) == limit {
			hasMore = true
			break
		}
		items = append(items, json.RawMessage(raw))
		next = encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: created, ID: key})
	}
	if rows.Err() != nil {
		heroFailure(c)
		return
	}
	if !hasMore {
		next = nil
	}
	heroData(c, gin.H{"items": items, "nextCursor": next})
}

func (a *adminAPI) getMonnifySettings(c *gin.Context) {
	s, e := monnifysettings.Read(c.Request.Context(), a.pool, 0)
	if e != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"settings": s, "configured": s.Configured(), "apiKeyConfigured": len(s.APIKey) > 0, "secretKeyConfigured": len(s.SecretKey) > 0, "contractCodeConfigured": len(s.ContractCode) > 0, "encryptionReady": len(a.settingsKey) == 32, "liveAllowed": false})
}
func (a *adminAPI) putMonnifySettings(c *gin.Context) {
	if len(a.settingsKey) != 32 {
		driverError(c, 503, "settings_encryption_unavailable", "Monnify settings encryption is unavailable.")
		return
	}
	var b struct {
		Version             int64  `json:"version"`
		Enabled             bool   `json:"enabled"`
		TripPaymentsEnabled bool   `json:"tripPaymentsEnabled"`
		Environment         string `json:"environment"`
		APIKey              string `json:"apiKey"`
		SecretKey           string `json:"secretKey"`
		ContractCode        string `json:"contractCode"`
	}
	if c.ShouldBindJSON(&b) != nil || b.Version < 0 || b.Environment != "sandbox" || b.TripPaymentsEnabled && !b.Enabled {
		driverError(c, 400, "invalid_monnify_settings", "Use sandbox settings. Enable Monnify before enabling trip collection.")
		return
	}
	for _, v := range []string{b.APIKey, b.SecretKey, b.ContractCode} {
		if len(v) > 2048 || strings.ContainsAny(v, "\r\n") {
			driverError(c, 400, "invalid_monnify_credentials", "Use valid Monnify credentials.")
			return
		}
	}
	ctx := c.Request.Context()
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('monnify-settings'))`); e != nil {
		heroFailure(c)
		return
	}
	s, e := monnifysettings.Read(ctx, tx, 0)
	if e != nil {
		heroFailure(c)
		return
	}
	if s.Version != b.Version {
		driverError(c, 409, "stale_monnify_settings", "Reload settings before saving.")
		return
	}
	for _, field := range []struct {
		value, purpose string
		target         *[]byte
	}{{b.APIKey, "api-key", &s.APIKey}, {b.SecretKey, "secret-key", &s.SecretKey}, {b.ContractCode, "contract-code", &s.ContractCode}} {
		if strings.TrimSpace(field.value) != "" {
			*field.target, e = monnifysettings.Seal(a.settingsKey, strings.TrimSpace(field.value), field.purpose)
			if e != nil {
				heroFailure(c)
				return
			}
		}
	}
	if b.Enabled && !s.Configured() {
		driverError(c, 400, "monnify_credentials_required", "Configure all three credentials before enabling Monnify.")
		return
	}
	if s.Environment != b.Environment && (!s.Configured() || b.APIKey == "" || b.SecretKey == "" || b.ContractCode == "") {
		driverError(c, 400, "replace_environment_credentials", "Supply all credentials when changing environment.")
		return
	}
	_, e = tx.Exec(ctx, `INSERT INTO admin_monnify_settings(enabled,trip_payments_enabled,environment,api_key_ciphertext,secret_key_ciphertext,contract_code_ciphertext,updated_by)VALUES($1,$2,$3,$4,$5,$6,$7::uuid)`, b.Enabled, b.TripPaymentsEnabled, b.Environment, s.APIKey, s.SecretKey, s.ContractCode, currentAdmin(c).ID)
	if e == nil {
		e = adminAudit(c, tx, "monnify.settings.updated", "", gin.H{"previousVersion": b.Version, "enabled": b.Enabled, "tripPaymentsEnabled": b.TripPaymentsEnabled, "environment": b.Environment, "apiKeyChanged": b.APIKey != "", "secretKeyChanged": b.SecretKey != "", "contractCodeChanged": b.ContractCode != ""})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		heroFailure(c)
		return
	}
	a.getMonnifySettings(c)
}
func (a *adminAPI) testMonnifySettings(c *gin.Context) {
	var result struct {
		Data struct {
			Authenticated bool   `json:"authenticated"`
			Environment   string `json:"environment"`
			Version       int64  `json:"version"`
		} `json:"data"`
	}
	if paymentInternalRequest(c.Request.Context(), "/internal/monnify/test", gin.H{}, &result) != nil {
		driverError(c, 503, "monnify_test_failed", "Could not authenticate with the configured Monnify account. Check payment-service access and credentials.")
		return
	}
	heroData(c, result.Data)
}
