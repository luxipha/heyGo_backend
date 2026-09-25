package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/cprakhar/uber-clone/shared/messaging"
	"github.com/cprakhar/uber-clone/shared/statutory"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type applicableChargeRule struct {
	id, code, name, authority, jurisdiction, kind, base, country, region, regionMatch, market, tag, packageSlug, bearer, funding string
	config                                                                                                                       []byte
	minimum, maximum                                                                                                             *int64
	reference, notes                                                                                                             string
	effectiveFrom                                                                                                                time.Time
	effectiveUntil                                                                                                               *time.Time
	version                                                                                                                      int
}

func assessTripStatutoryCharges(ctx context.Context, tx pgx.Tx, tripID, driverID string) error {
	var fare int64
	var marketCode, originCode, destinationCode *string
	var packageSlug string
	var tags []string
	var geofences []byte
	if err := tx.QueryRow(ctx, `SELECT ROUND(f.total_fare_minor)::BIGINT,t.market_code,t.origin_region_code,t.destination_region_code,
		f.package_slug,t.regulatory_tags,COALESCE(t.classification_geofences,'{}'::JSONB)
		FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id WHERE t.id=$1::UUID`, tripID).
		Scan(&fare, &marketCode, &originCode, &destinationCode, &packageSlug, &tags, &geofences); err != nil {
		return err
	}
	market, origin, destination := sqlNullableString(marketCode), sqlNullableString(originCode), sqlNullableString(destinationCode)
	rows, err := tx.Query(ctx, `SELECT id::TEXT,ledger_code,version,name,authority,jurisdiction,calculation_type,calculation_base,
		calculation_config,COALESCE(country_code,''),COALESCE(region_code,''),region_match,COALESCE(market_code,''),COALESCE(regulatory_tag,''),COALESCE(vehicle_package,''),bearer,funding_source,
		minimum_kobo,maximum_kobo,reference,notes,effective_from,effective_until
		FROM statutory_charge_rules r WHERE status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())
		AND (country_code IS NULL OR country_code='NG') AND (market_code IS NULL OR market_code=$1)
		AND (region_code IS NULL OR (region_match='pickup' AND region_code=$2) OR (region_match='destination' AND region_code=$3)
		 OR (region_match='either' AND region_code IN ($2,$3)) OR (region_match='both' AND $2=$3 AND region_code=$2))
		AND (r.regulatory_tag IS NULL OR EXISTS(SELECT 1 FROM trips t2,UNNEST(t2.regulatory_tags) AS tag
			WHERE t2.id=$4::UUID AND tag=r.regulatory_tag))
		AND (vehicle_package IS NULL OR vehicle_package=$5)
		ORDER BY ledger_code,id`, market, origin, destination, tripID, packageSlug)
	if err != nil {
		return err
	}
	var rules []applicableChargeRule
	for rows.Next() {
		var r applicableChargeRule
		if err := rows.Scan(&r.id, &r.code, &r.version, &r.name, &r.authority, &r.jurisdiction, &r.kind, &r.base, &r.config, &r.country, &r.region, &r.regionMatch, &r.market, &r.tag, &r.packageSlug, &r.bearer, &r.funding, &r.minimum, &r.maximum, &r.reference, &r.notes, &r.effectiveFrom, &r.effectiveUntil); err != nil {
			rows.Close()
			return err
		}
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(rules) == 0 {
		return nil
	}
	// Lock balance once and honor the approved market floor policy. Trip completion
	// itself is never rolled back for an insufficient balance.
	if _, err := tx.Exec(ctx, `INSERT INTO driver_operating_accounts(driver_id) VALUES($1::UUID) ON CONFLICT DO NOTHING`, driverID); err != nil {
		return err
	}
	var balance, floor int64
	if err := tx.QueryRow(ctx, `SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::UUID FOR UPDATE`, driverID).Scan(&balance); err != nil {
		return err
	}
	var policyCount int
	var allowNegative bool
	var maxNegative int64
	if err := tx.QueryRow(ctx, `SELECT COUNT(*),COALESCE(BOOL_AND(allow_negative),FALSE),COALESCE(MIN(maximum_negative_kobo),0)
		FROM operating_balance_policies WHERE market_code=$1 AND status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())`, market).Scan(&policyCount, &allowNegative, &maxNegative); err != nil {
		return err
	}
	if policyCount == 1 && allowNegative {
		floor = -maxNegative
	}
	for _, r := range rules {
		snapshot := map[string]any{"id": r.id, "ledgerCode": r.code, "version": r.version, "name": r.name, "authority": r.authority, "jurisdiction": r.jurisdiction, "calculationType": r.kind, "calculationBase": r.base, "calculationConfig": json.RawMessage(r.config), "minimumKobo": r.minimum, "maximumKobo": r.maximum, "countryCode": r.country, "regionCode": r.region, "marketSelector": r.market, "regulatoryTag": r.tag, "vehiclePackage": r.packageSlug, "marketCode": market, "originRegionCode": origin, "destinationRegionCode": destination, "regulatoryTags": tags, "classificationGeofences": json.RawMessage(geofences), "bearer": r.bearer, "fundingSource": r.funding, "regionMatch": r.regionMatch, "reference": r.reference, "notes": r.notes, "effectiveFrom": r.effectiveFrom, "effectiveUntil": r.effectiveUntil}
		ruleSnapshot, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		amount, err := statutory.Calculate(statutory.Rule{Type: r.kind, Base: r.base, Config: r.config, Minimum: r.minimum, Maximum: r.maximum}, fare)
		if err != nil {
			return fmt.Errorf("rule %s: %w", r.code, err)
		}
		chargeID := uuid.NewString()
		if _, err := tx.Exec(ctx, `INSERT INTO trip_statutory_charges(id,trip_id,rule_id,driver_id,amount_kobo,bearer,funding_source,rule_snapshot)
			VALUES($1::UUID,$2::UUID,$3::UUID,$4::UUID,$5,$6,$7,$8::JSONB) ON CONFLICT(trip_id,rule_id) DO NOTHING`, chargeID, tripID, r.id, driverID, amount, r.bearer, r.funding, ruleSnapshot); err != nil {
			return err
		}
		var inserted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trip_statutory_charges WHERE id=$1::UUID)`, chargeID).Scan(&inserted); err != nil {
			return err
		}
		if !inserted {
			continue
		}
		if amount == 0 || r.bearer != "driver" || r.funding != "operating_balance" {
			continue
		}
		capacity := new(big.Int).Sub(big.NewInt(balance), big.NewInt(floor))
		debit := amount
		if capacity.Sign() <= 0 {
			debit = 0
		} else if capacity.Cmp(big.NewInt(debit)) < 0 {
			debit = capacity.Int64()
		}
		if debit > 0 {
			balance -= debit
			entryID := uuid.NewString()
			if _, err := tx.Exec(ctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key,details)
				VALUES($1::UUID,$2::UUID,'statutory_charge',$3,$4,$5,jsonb_build_object('tripId',$6::TEXT,'chargeId',$7::TEXT,'ledgerCode',$8::TEXT))`, entryID, driverID, -debit, balance, "statutory:"+chargeID, tripID, chargeID, r.code); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE driver_operating_accounts SET balance_kobo=$2,updated_at=NOW() WHERE driver_id=$1::UUID`, driverID, balance); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE trip_statutory_charges SET operating_entry_id=$2::UUID WHERE id=$1::UUID`, chargeID, entryID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO statutory_charge_repayments(id,charge_id,driver_id,amount_kobo,operating_entry_id) VALUES($1::UUID,$2::UUID,$3::UUID,$4,$5::UUID)`, uuid.NewString(), chargeID, driverID, debit, entryID); err != nil {
				return err
			}
			notificationData, err := json.Marshal(map[string]any{"kind": "statutory_charge", "deltaKobo": -debit, "balanceAfterKobo": balance, "tripId": tripID, "chargeId": chargeID, "ledgerCode": r.code})
			if err != nil {
				return err
			}
			if _, err := messaging.AppendEventTx(ctx, tx, driverID, "statutory-charge:"+chargeID, "driver.operating_balance.updated", notificationData); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(ctx, `UPDATE drivers SET status=CASE WHEN online_requested AND driver_balance_eligible(id,online_market_code) AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=drivers.id),FALSE) THEN 'available' ELSE 'offline' END,
		available=online_requested AND driver_balance_eligible(id,online_market_code) AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=drivers.id),FALSE),updated_at=NOW() WHERE id=$1::UUID`, driverID)
	return err
}

func sqlNullableString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}
