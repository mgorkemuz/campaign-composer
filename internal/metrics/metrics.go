// Package metrics exposes Prometheus metrics on /metrics: provider calls and
// latency, delivery outcomes, webhooks, LLM calls and tokens, and queue depth.
package metrics

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry        *prometheus.Registry
	providerCalls   *prometheus.CounterVec
	providerSeconds *prometheus.HistogramVec
	deliveries      *prometheus.CounterVec
	webhooks        *prometheus.CounterVec
	llmCalls        *prometheus.CounterVec
	llmTokens       *prometheus.CounterVec
}

func New(pool *pgxpool.Pool) *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		providerCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "composer_provider_requests_total", Help: "Send requests to the SMS/email provider, by HTTP status (0 = network error).",
		}, []string{"channel", "code"}),
		providerSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "composer_provider_request_seconds", Help: "Provider send latency.",
			Buckets: []float64{.025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"channel"}),
		deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "composer_deliveries_finished_total", Help: "Deliveries finished by our send call: sent (accepted by the provider) or failed (gave up). A retry made unnecessary by an early receipt is not counted.",
		}, []string{"channel", "status"}),
		webhooks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "composer_webhooks_total", Help: "Delivery receipts received, by outcome (applied, ignored, rejected).",
		}, []string{"outcome"}),
		llmCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "composer_llm_calls_total", Help: "LLM calls by task and outcome.",
		}, []string{"task", "outcome"}),
		llmTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "composer_llm_tokens_total", Help: "LLM tokens by task and direction (input, output).",
		}, []string{"task", "direction"}),
	}
	m.registry.MustRegister(m.providerCalls, m.providerSeconds, m.deliveries, m.webhooks, m.llmCalls, m.llmTokens,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	if pool != nil {
		m.registry.MustRegister(&queueCollector{pool: pool})
	}
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ProviderCall implements delivery.Observer.
func (m *Metrics) ProviderCall(channel string, status int, d time.Duration) {
	m.providerCalls.WithLabelValues(channel, strconv.Itoa(status)).Inc()
	m.providerSeconds.WithLabelValues(channel).Observe(d.Seconds())
}

// DeliveryFinished implements delivery.Observer.
func (m *Metrics) DeliveryFinished(channel, status string) {
	m.deliveries.WithLabelValues(channel, status).Inc()
}

func (m *Metrics) Webhook(outcome string) { m.webhooks.WithLabelValues(outcome).Inc() }

// LLMCall records one model call.
func (m *Metrics) LLMCall(task, outcome string, inputTokens, outputTokens int) {
	m.llmCalls.WithLabelValues(task, outcome).Inc()
	m.llmTokens.WithLabelValues(task, "input").Add(float64(inputTokens))
	m.llmTokens.WithLabelValues(task, "output").Add(float64(outputTokens))
}

// queueCollector reads queue depth from the jobs table at scrape time, so the
// numbers are true across processes and restarts.
type queueCollector struct{ pool *pgxpool.Pool }

var queueDesc = prometheus.NewDesc("composer_queue_jobs", "Jobs waiting or running, by kind and status.", []string{"kind", "status"}, nil)

func (c *queueCollector) Describe(ch chan<- *prometheus.Desc) { ch <- queueDesc }

func (c *queueCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := c.pool.Query(ctx, `SELECT kind, status, count(*) FROM jobs WHERE status IN ('queued', 'running') GROUP BY 1, 2`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var kind, status string
		var n float64
		if rows.Scan(&kind, &status, &n) == nil {
			ch <- prometheus.MustNewConstMetric(queueDesc, prometheus.GaugeValue, n, kind, status)
		}
	}
}
