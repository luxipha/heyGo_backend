package handler

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const heroBankNumberPurpose = "heroes-payout-account-v1"
const heroBankNamePurpose = "heroes-payout-name-v1"
const heroEarnedSQL = `COALESCE((SELECT sum(w.amount_kobo) FROM heroes_wallet_entries w WHERE w.hero_id=h.id),0)`
const heroPaidSQL = `COALESCE((SELECT sum(p.amount_kobo) FROM heroes_manual_payments p WHERE p.hero_id=h.id AND NOT EXISTS(SELECT 1 FROM heroes_payment_reversals v WHERE v.payment_id=p.id)),0)`

type heroBank struct {
	ID            string    `json:"id"`
	BankName      string    `json:"bankName"`
	AccountName   string    `json:"accountName"`
	MaskedNumber  string    `json:"maskedNumber"`
	AccountNumber string    `json:"accountNumber,omitempty"`
	ProvidedAt    time.Time `json:"providedAt"`
	Verification  string    `json:"verification"`
}

func (a *heroesAPI) readBank(c *gin.Context, id string, full bool) (*heroBank, error) {
	var b heroBank
	var number, name []byte
	err := a.pool.QueryRow(c.Request.Context(), `SELECT b.id::text,b.bank_name,b.account_name_ciphertext,b.account_number_ciphertext,b.account_number_masked,b.created_at FROM heroes_accounts h JOIN heroes_bank_accounts b ON b.id=h.payout_account_id AND b.hero_id=h.id WHERE h.id=$1::uuid`, id).Scan(&b.ID, &b.BankName, &name, &number, &b.MaskedNumber, &b.ProvidedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.AccountName, err = openAdminValue(a.email.settingsKey, name, heroBankNamePurpose)
	if err != nil {
		return nil, err
	}
	if full {
		b.AccountNumber, err = openAdminValue(a.email.settingsKey, number, heroBankNumberPurpose)
	}
	b.Verification = "hero_provided"
	return &b, err
}
func (a *heroesAPI) saveBank(c *gin.Context) {
	var b struct {
		BankName      string `json:"bankName"`
		AccountName   string `json:"accountName"`
		AccountNumber string `json:"accountNumber"`
	}
	if c.ShouldBindJSON(&b) != nil {
		driverError(c, 400, "invalid_bank", "Enter your bank and account details")
		return
	}
	b.BankName = strings.TrimSpace(b.BankName)
	b.AccountName = strings.TrimSpace(b.AccountName)
	b.AccountNumber = strings.TrimSpace(b.AccountNumber)
	valid := len(b.AccountNumber) == 10
	for _, r := range b.AccountNumber {
		valid = valid && r >= '0' && r <= '9'
	}
	if !valid || b.BankName == "" || b.AccountName == "" || utf8.RuneCountInString(b.BankName) > 100 || utf8.RuneCountInString(b.AccountName) > 100 {
		driverError(c, 400, "invalid_bank", "Enter a bank, account name and 10-digit account number")
		return
	}
	number, err := sealAdminValue(a.email.settingsKey, b.AccountNumber, heroBankNumberPurpose)
	if err != nil {
		heroFailure(c)
		return
	}
	name, err := sealAdminValue(a.email.settingsKey, b.AccountName, heroBankNamePurpose)
	if err != nil {
		heroFailure(c)
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	_, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "hero-money:"+heroID(c))
	id := uuid.NewString()
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `INSERT INTO heroes_bank_accounts(id,hero_id,bank_name,account_name_ciphertext,account_number_ciphertext,account_number_masked)VALUES($1::uuid,$2::uuid,$3,$4,$5,$6)`, id, heroID(c), b.BankName, name, number, "******"+b.AccountNumber[6:])
	}
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `UPDATE heroes_accounts SET payout_account_id=$2::uuid WHERE id=$1::uuid`, heroID(c), id)
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"saved": true})
}
func (a *heroesAPI) payments(c *gin.Context, id string) ([]gin.H, error) {
	rows, err := a.pool.Query(c.Request.Context(), `SELECT p.id::text,p.amount_kobo,p.reference,p.paid_at,p.created_at,u.username,b.bank_name,b.account_number_masked,v.reason,v.created_at FROM heroes_manual_payments p JOIN admin_users u ON u.id=p.recorded_by JOIN heroes_bank_accounts b ON b.id=p.bank_account_id LEFT JOIN heroes_payment_reversals v ON v.payment_id=p.id WHERE p.hero_id=$1::uuid ORDER BY p.created_at DESC,p.id DESC LIMIT 100`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []gin.H{}
	for rows.Next() {
		var pid, ref, by, bank, masked string
		var amount int64
		var paid, created time.Time
		var reason *string
		var reversedAt *time.Time
		if err = rows.Scan(&pid, &amount, &ref, &paid, &created, &by, &bank, &masked, &reason, &reversedAt); err != nil {
			return nil, err
		}
		items = append(items, gin.H{"id": pid, "amountKobo": amount, "reference": ref, "paidAt": paid, "recordedAt": created, "recordedBy": by, "bankName": bank, "maskedNumber": masked, "reversed": reason != nil, "reversalReason": reason, "reversedAt": reversedAt})
	}
	return items, rows.Err()
}
func (a *adminAPI) heroesPaymentsList(c *gin.Context) {
	offset, _ := strconv.Atoi(c.Query("offset"))
	if offset < 0 {
		offset = 0
	}
	query := strings.TrimSpace(c.Query("search"))
	if len(query) > 100 {
		driverError(c, 400, "invalid_search", "Search is too long")
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `SELECT h.id::text,h.name,h.email,h.referral_code,`+heroEarnedSQL+`,`+heroPaidSQL+`,h.payout_account_id IS NOT NULL FROM heroes_accounts h WHERE ($1='' OR strpos(lower(h.name||' '||h.email||' '||h.referral_code),lower($1))>0) ORDER BY (`+heroEarnedSQL+`-`+heroPaidSQL+`) DESC,h.created_at,h.id LIMIT 26 OFFSET $2`, query, offset)
	if err != nil {
		heroFailure(c)
		return
	}
	defer rows.Close()
	items := []gin.H{}
	for rows.Next() {
		var id, name, email, code string
		var earned, paid int64
		var bank bool
		if err = rows.Scan(&id, &name, &email, &code, &earned, &paid, &bank); err != nil {
			heroFailure(c)
			return
		}
		items = append(items, gin.H{"id": id, "name": name, "email": email, "code": code, "earnedKobo": earned, "paidKobo": paid, "unpaidKobo": earned - paid, "hasBank": bank})
	}
	if rows.Err() != nil {
		heroFailure(c)
		return
	}
	more := len(items) > 25
	if more {
		items = items[:25]
	}
	heroData(c, gin.H{"items": items, "hasMore": more, "offset": offset})
}
func (a *adminAPI) heroesPaymentDetail(c *gin.Context) {
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		driverError(c, 400, "invalid_hero", "Invalid Hero")
		return
	}
	var name, email string
	var earned, paid int64
	err := a.pool.QueryRow(c.Request.Context(), `SELECT h.name,h.email,`+heroEarnedSQL+`,`+heroPaidSQL+` FROM heroes_accounts h WHERE h.id=$1::uuid`, id).Scan(&name, &email, &earned, &paid)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "hero_not_found", "Hero not found")
		return
	}
	if err != nil {
		heroFailure(c)
		return
	}
	api := &heroesAPI{pool: a.pool, email: a}
	bank, err := api.readBank(c, id, adminHasPermission(c, permissionFinanceWrite))
	if err != nil {
		heroFailure(c)
		return
	}
	history, err := api.payments(c, id)
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"id": id, "name": name, "email": email, "earnedKobo": earned, "paidKobo": paid, "unpaidKobo": earned - paid, "bankAccount": bank, "payments": history})
}
func (a *adminAPI) recordHeroPayment(c *gin.Context) {
	id := c.Param("id")
	var b struct {
		ID        string    `json:"id"`
		Amount    int64     `json:"amountKobo"`
		Reference string    `json:"reference"`
		PaidAt    time.Time `json:"paidAt"`
		BankID    string    `json:"bankAccountId"`
	}
	if c.ShouldBindJSON(&b) != nil {
		driverError(c, 400, "invalid_payment", "Enter payment amount, date and reference")
		return
	}
	b.Reference = strings.ToLower(strings.TrimSpace(b.Reference))
	_, e1 := uuid.Parse(id)
	_, e2 := uuid.Parse(b.ID)
	_, e3 := uuid.Parse(b.BankID)
	if e1 != nil || e2 != nil || e3 != nil || b.Amount <= 0 || len(b.Reference) < 3 || len(b.Reference) > 120 || b.PaidAt.IsZero() || b.PaidAt.After(time.Now().Add(time.Minute)) {
		driverError(c, 400, "invalid_payment", "Enter a valid amount, transfer date and unique reference")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "hero-money:"+id); err != nil {
		heroFailure(c)
		return
	}
	var oldHero, oldRef, oldBank string
	var oldAmount int64
	var oldAt time.Time
	err = tx.QueryRow(c.Request.Context(), `SELECT hero_id::text,reference,bank_account_id::text,amount_kobo,paid_at FROM heroes_manual_payments WHERE id=$1::uuid`, b.ID).Scan(&oldHero, &oldRef, &oldBank, &oldAmount, &oldAt)
	if err == nil {
		if oldHero != id || oldRef != b.Reference || oldBank != b.BankID || oldAmount != b.Amount || !oldAt.Equal(b.PaidAt) {
			driverError(c, 409, "payment_changed", "This request was already used for another payment")
			return
		}
		heroData(c, gin.H{"recorded": true, "id": b.ID})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		heroFailure(c)
		return
	}
	var earned, paid int64
	var bank *string
	err = tx.QueryRow(c.Request.Context(), `SELECT `+heroEarnedSQL+`,`+heroPaidSQL+`,h.payout_account_id::text FROM heroes_accounts h WHERE h.id=$1::uuid`, id).Scan(&earned, &paid, &bank)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "hero_not_found", "Hero not found")
		return
	}
	if err != nil {
		heroFailure(c)
		return
	}
	if bank == nil || *bank != b.BankID {
		driverError(c, 409, "bank_changed", "Reload and confirm the current bank details before recording payment")
		return
	}
	if b.Amount > earned-paid {
		driverError(c, 409, "amount_exceeds_unpaid", "Payment exceeds this Hero's unpaid earnings")
		return
	}
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO heroes_manual_payments(id,hero_id,bank_account_id,amount_kobo,reference,paid_at,recorded_by)VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7::uuid)`, b.ID, id, b.BankID, b.Amount, b.Reference, b.PaidAt, currentAdmin(c).ID)
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `INSERT INTO heroes_payment_references(reference,payment_id)VALUES($1,$2::uuid)`, b.Reference, b.ID)
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		driverError(c, 409, "duplicate_payment", "This transfer reference is already recorded")
		return
	}
	if err == nil {
		err = adminAudit(c, tx, "heroes.payment.recorded", "", gin.H{"heroId": id, "heroPaymentId": b.ID, "paymentId": b.ID, "amountKobo": b.Amount, "reference": b.Reference})
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"recorded": true, "id": b.ID})
}
func (a *adminAPI) reverseHeroPayment(c *gin.Context) {
	id := c.Param("id")
	var b struct {
		Reason string `json:"reason"`
	}
	if _, err := uuid.Parse(id); err != nil {
		driverError(c, 400, "invalid_payment", "Invalid payment")
		return
	}
	if c.ShouldBindJSON(&b) != nil || len(strings.TrimSpace(b.Reason)) < 5 || len(b.Reason) > 500 {
		driverError(c, 400, "reason_required", "Enter a correction reason of 5–500 characters")
		return
	}
	var hero string
	if err := a.pool.QueryRow(c.Request.Context(), `SELECT hero_id::text FROM heroes_manual_payments WHERE id=$1::uuid`, id).Scan(&hero); err != nil {
		driverError(c, 404, "payment_not_found", "Payment not found")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	_, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "hero-money:"+hero)
	if err == nil {
		var tag pgconn.CommandTag
		tag, err = tx.Exec(c.Request.Context(), `INSERT INTO heroes_payment_reversals(payment_id,reason,recorded_by)VALUES($1::uuid,$2,$3::uuid) ON CONFLICT(payment_id)DO NOTHING`, id, strings.TrimSpace(b.Reason), currentAdmin(c).ID)
		if err == nil && tag.RowsAffected() == 0 {
			heroData(c, gin.H{"corrected": true})
			return
		}
	}
	if err == nil {
		_, err = tx.Exec(c.Request.Context(), `DELETE FROM heroes_payment_references WHERE payment_id=$1::uuid`, id)
	}
	if err == nil {
		err = adminAudit(c, tx, "heroes.payment.corrected", "", gin.H{"heroId": hero, "heroPaymentId": id, "paymentId": id, "reason": strings.TrimSpace(b.Reason)})
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"corrected": true})
}
