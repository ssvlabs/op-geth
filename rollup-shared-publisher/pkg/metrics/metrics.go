package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Uptime metric - used by the collector.
var (
	Uptime = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "publisher_uptime_seconds",
		Help: "Uptime in seconds",
	})
)
