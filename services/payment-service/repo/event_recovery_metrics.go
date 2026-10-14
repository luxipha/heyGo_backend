package repo

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

type paymentBacklogCollector struct {
	pool                                   *pgxpool.Pool
	pending, age, unrecoverable, available *prometheus.Desc
}

func NewPaymentBacklogCollector(pool *pgxpool.Pool) prometheus.Collector {
	return &paymentBacklogCollector{pool: pool,
		pending:       prometheus.NewDesc("heygo_payment_events_pending", "Unpublished payment webhook events.", nil, nil),
		age:           prometheus.NewDesc("heygo_payment_events_oldest_age_seconds", "Age of the oldest unpublished payment webhook event.", nil, nil),
		unrecoverable: prometheus.NewDesc("heygo_payment_events_unrecoverable", "Unpublished legacy events without a complete recovery envelope.", nil, nil),
		available:     prometheus.NewDesc("heygo_payment_events_backlog_read_success", "Whether the database backlog read succeeded.", nil, nil),
	}
}

func (c *paymentBacklogCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.pending, c.age, c.unrecoverable, c.available} {
		ch <- d
	}
}

func (c *paymentBacklogCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var count, age, missing float64
	err := c.pool.QueryRow(ctx, `SELECT COUNT(*),
		COALESCE(GREATEST(EXTRACT(EPOCH FROM NOW()-MIN(processed_at)),0),0),
		COUNT(*) FILTER (WHERE topic IS NULL OR entity_id IS NULL OR payload IS NULL)
		FROM payment_webhook_events WHERE NOT published`).Scan(&count, &age, &missing)
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.available, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.available, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(c.pending, prometheus.GaugeValue, count)
	ch <- prometheus.MustNewConstMetric(c.age, prometheus.GaugeValue, age)
	ch <- prometheus.MustNewConstMetric(c.unrecoverable, prometheus.GaugeValue, missing)
}
