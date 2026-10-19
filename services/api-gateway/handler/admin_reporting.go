package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

// These aggregates cover all matching database rows, independent of list pagination.
const supportSummarySQL = `WITH cases AS (SELECT c.*, EXISTS(SELECT 1 FROM support_case_escalations e WHERE e.case_id=c.id) AS escalated FROM support_cases c)
SELECT jsonb_build_object(
 'asOf',NOW(),'scope','All recorded support cases',
 'total',(SELECT COUNT(*) FROM cases),
 'open',(SELECT COUNT(*) FROM cases WHERE status NOT IN ('resolved','closed')),
 'pendingResponse',(SELECT COUNT(*) FROM cases c WHERE status IN ('open','in_progress') AND COALESCE((SELECT sender_kind FROM support_case_messages m WHERE m.case_id=c.id ORDER BY created_at DESC,id DESC LIMIT 1),'user')='user'),
 'escalated',(SELECT COUNT(*) FROM cases WHERE escalated AND status NOT IN ('resolved','closed')),
 'resolved',(SELECT COUNT(*) FROM cases WHERE status IN ('resolved','closed')),
 'averageFirstResponseMinutes',(SELECT AVG(EXTRACT(EPOCH FROM (first_reply-created_at))/60) FROM (SELECT c.created_at,(SELECT MIN(m.created_at) FROM support_case_messages m WHERE m.case_id=c.id AND m.sender_kind='staff') first_reply FROM cases c) r WHERE first_reply>=created_at),
 'categories',COALESCE((SELECT jsonb_agg(to_jsonb(q)) FROM (SELECT c.topic_code AS code,t.label,COUNT(*) AS count FROM cases c JOIN support_topics t ON t.code=c.topic_code GROUP BY c.topic_code,t.label ORDER BY COUNT(*) DESC,c.topic_code) q),'[]'::jsonb),
 'workload',COALESCE((SELECT jsonb_agg(to_jsonb(q)) FROM (SELECT a.id,a.username,COUNT(*) FILTER(WHERE c.status='open') AS open,COUNT(*) FILTER(WHERE c.status IN ('in_progress','waiting_on_user')) AS progress,COUNT(*) FILTER(WHERE c.status IN ('resolved','closed')) AS resolved FROM cases c JOIN admin_users a ON a.id=c.assigned_admin_id GROUP BY a.id,a.username ORDER BY COUNT(*) DESC,a.id LIMIT 10) q),'[]'::jsonb),
 'recent',COALESCE((SELECT jsonb_agg(to_jsonb(q)) FROM (SELECT id,subject,status,updated_at AS "updatedAt" FROM cases ORDER BY updated_at DESC,id DESC LIMIT 5) q),'[]'::jsonb),
 'alerts',COALESCE((SELECT jsonb_agg(to_jsonb(q)) FROM (SELECT id,subject,status,priority FROM cases WHERE status NOT IN ('resolved','closed') AND (priority='high' OR escalated) ORDER BY updated_at DESC,id DESC LIMIT 5) q),'[]'::jsonb),
 'financeReferrals',(SELECT COUNT(*) FROM support_case_outcomes WHERE outcome='referred_to_finance'),
 'csat',NULL,'sla',NULL,'channels',NULL)`

func (a *supportAPI) adminSummary(c *gin.Context) {
	var data json.RawMessage
	if err := a.pool.QueryRow(c.Request.Context(), supportSummarySQL).Scan(&data); err != nil {
		driverError(c, http.StatusServiceUnavailable, "support_summary_unavailable", "Support summary is unavailable")
		return
	}
	c.JSON(http.StatusOK, contracts.APIResponse{Data: data})
}

const analyticsSummarySQL = `WITH selected AS (
 SELECT t.*,ROUND(f.total_fare_minor)::BIGINT AS fare_kobo FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id
 WHERE t.created_at >= $1 AND t.created_at < $2 AND ($3='' OR t.market_code=$3)
), summary AS (
 SELECT COUNT(*) AS trips, COUNT(*) FILTER(WHERE status='completed') AS completed,COUNT(*) FILTER(WHERE status='cancelled') AS cancelled,
 COUNT(DISTINCT rider_id) AS riders,COUNT(DISTINCT assigned_driver_id) AS partners,
 COALESCE(SUM(fare_kobo) FILTER(WHERE status='completed'),0) AS gross FROM selected
)
SELECT jsonb_build_object('asOf',$2::timestamptz,'from',$1::timestamptz,'to',$2::timestamptz,'marketCode',$3::text,
 'totalTrips',s.trips,'completedTrips',s.completed,'cancelledTrips',s.cancelled,'activeRiders',s.riders,'activePartners',s.partners,'completedFareKobo',s.gross,
 'completionRate',CASE WHEN s.trips=0 THEN NULL ELSE ROUND(100.0*s.completed/s.trips,2) END,
 'daily',COALESCE((SELECT jsonb_agg(to_jsonb(d) ORDER BY d.day) FROM (SELECT (created_at AT TIME ZONE 'UTC')::date AS day,COUNT(*) AS trips,COALESCE(SUM(fare_kobo) FILTER(WHERE status='completed'),0) AS "fareKobo" FROM selected GROUP BY 1) d),'[]'::jsonb),
 'markets',COALESCE((SELECT jsonb_agg(to_jsonb(m)) FROM (SELECT COALESCE(market_code,'Unclassified') AS code,COUNT(*) AS trips,COUNT(DISTINCT assigned_driver_id) AS partners,COALESCE(SUM(fare_kobo) FILTER(WHERE status='completed'),0) AS "fareKobo",ROUND(100.0*COUNT(*) FILTER(WHERE status='completed')/COUNT(*),2) AS "completionRate" FROM selected GROUP BY market_code ORDER BY COUNT(*) DESC,COALESCE(market_code,'Unclassified')) m),'[]'::jsonb),
 'statuses',COALESCE((SELECT jsonb_agg(to_jsonb(q)) FROM (SELECT status,COUNT(*) AS count FROM selected GROUP BY status ORDER BY status) q),'[]'::jsonb),
 'repeatRiders',(SELECT COUNT(*) FROM (SELECT rider_id FROM selected GROUP BY rider_id HAVING COUNT(*)>1) r),
 'pickupEtaMinutes',NULL,'commissionKobo',NULL,'payoutKobo',NULL,'retentionRate',NULL
) FROM summary s`

func (a *adminAPI) analyticsSummary(c *gin.Context) {
	days := 30
	if raw := c.Query("days"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || (value != 7 && value != 30 && value != 90) {
			driverError(c, http.StatusBadRequest, "invalid_days", "Days must be 7, 30 or 90")
			return
		}
		days = value
	}
	market := c.Query("marketCode")
	if len(market) > 80 {
		driverError(c, http.StatusBadRequest, "invalid_market", "Market code is too long")
		return
	}
	now := time.Now().UTC()
	var data json.RawMessage
	if err := a.pool.QueryRow(c.Request.Context(), analyticsSummarySQL, now.AddDate(0, 0, -days), now, market).Scan(&data); err != nil {
		driverError(c, http.StatusServiceUnavailable, "analytics_unavailable", "Analytics are unavailable")
		return
	}
	c.JSON(http.StatusOK, contracts.APIResponse{Data: data})
}

const safetySummarySQL = `WITH incidents AS (
 SELECT i.*,c.subject,c.audience,EXISTS(SELECT 1 FROM sos_intakes s WHERE s.incident_id=i.id) AS sos FROM safety_incidents i JOIN support_cases c ON c.id=i.case_id
) SELECT jsonb_build_object('asOf',NOW(),'scope','All recorded safety incidents; today uses UTC',
 'total',(SELECT COUNT(*) FROM incidents),
 'active',(SELECT COUNT(*) FROM incidents WHERE status<>'resolved'),
 'open',(SELECT COUNT(*) FROM incidents WHERE status='open'),
 'sosToday',(SELECT COUNT(*) FROM sos_intakes WHERE created_at >= date_trunc('day',NOW() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AND created_at<=NOW()),
 'highRisk',(SELECT COUNT(*) FROM incidents WHERE status<>'resolved' AND severity IN ('high','critical')),
 'resolved',(SELECT COUNT(*) FROM incidents WHERE status='resolved'),
 'investigating',(SELECT COUNT(*) FROM incidents WHERE status='investigating'),
 'averageFirstActionMinutes',(SELECT AVG(EXTRACT(EPOCH FROM (first_action-created_at))/60) FROM (SELECT i.created_at,(SELECT MIN(a.created_at) FROM safety_incident_actions a WHERE a.incident_id=i.id) first_action FROM incidents i) r WHERE first_action>=created_at),
 'breakdown',COALESCE((SELECT jsonb_agg(to_jsonb(q)) FROM (SELECT severity AS label,COUNT(*) AS count FROM incidents GROUP BY severity ORDER BY COUNT(*) DESC,severity) q),'[]'::jsonb),
 'sources',COALESCE((SELECT jsonb_agg(to_jsonb(q)) FROM (SELECT audience AS label,COUNT(*) AS count FROM incidents GROUP BY audience ORDER BY audience) q),'[]'::jsonb),
 'workload',COALESCE((SELECT jsonb_agg(to_jsonb(q)) FROM (SELECT a.id,a.username,COUNT(*) FILTER(WHERE i.status='open') AS open,COUNT(*) FILTER(WHERE i.status IN ('acknowledged','investigating')) AS review,COUNT(*) FILTER(WHERE i.status='escalated') AS escalated,COUNT(*) FILTER(WHERE i.status='resolved') AS resolved FROM incidents i JOIN admin_users a ON a.id=i.assigned_admin_id GROUP BY a.id,a.username ORDER BY COUNT(*) DESC,a.id LIMIT 10) q),'[]'::jsonb),
 'activity',COALESCE((SELECT jsonb_agg(to_jsonb(q)) FROM (SELECT i.id,i.subject,a.new_status AS status,a.created_at AS "createdAt" FROM safety_incident_actions a JOIN incidents i ON i.id=a.incident_id ORDER BY a.created_at DESC,a.id DESC LIMIT 5) q),'[]'::jsonb),
 'alerts',COALESCE((SELECT jsonb_agg(to_jsonb(q)) FROM (SELECT id,subject,severity,status,sos FROM incidents WHERE status<>'resolved' AND (severity IN ('high','critical') OR sos) ORDER BY CASE WHEN severity='critical' THEN 0 WHEN sos THEN 1 ELSE 2 END,created_at DESC,id DESC LIMIT 5) q),'[]'::jsonb),
 'sla',NULL,'restrictions',NULL,'suspensions',NULL)`

func (a *adminAPI) safetySummary(c *gin.Context) {
	var data json.RawMessage
	if err := a.pool.QueryRow(c.Request.Context(), safetySummarySQL).Scan(&data); err != nil {
		driverError(c, http.StatusServiceUnavailable, "safety_summary_unavailable", "Safety summary is unavailable")
		return
	}
	c.JSON(http.StatusOK, contracts.APIResponse{Data: data})
}
