package gateway

import "github.com/prometheus/client_golang/prometheus"

var (
	mRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_requests_total", Help: "Requests by model, provider and HTTP status.",
	}, []string{"model", "provider", "status"})
	mLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "gateway_request_seconds", Help: "End-to-end request latency.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2, 5, 10, 30, 60},
	}, []string{"model"})
	mTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_tokens_total", Help: "Tokens by model and direction.",
	}, []string{"model", "direction"})
	mCacheHits = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gateway_cache_hits_total", Help: "Responses served from the response cache.",
	})
)

func init() {
	prometheus.MustRegister(mRequests, mLatency, mTokens, mCacheHits)
}
