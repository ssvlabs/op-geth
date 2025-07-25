package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ComponentRegistry manages metrics for a specific component.
type ComponentRegistry struct {
	namespace string
	subsystem string
	registry  *prometheus.Registry
}

// NewComponentRegistry creates a registry for a component.
func NewComponentRegistry(namespace, subsystem string) *ComponentRegistry {
	return &ComponentRegistry{
		namespace: namespace,
		subsystem: subsystem,
		registry:  prometheus.NewRegistry(),
	}
}

// NewCounterVec creates a new counter with proper naming.
func (r *ComponentRegistry) NewCounterVec(opts prometheus.CounterOpts, labelNames []string) *prometheus.CounterVec {
	opts.Namespace = r.namespace
	opts.Subsystem = r.subsystem
	return promauto.NewCounterVec(opts, labelNames)
}

// NewCounter creates a new counter with proper naming.
func (r *ComponentRegistry) NewCounter(opts prometheus.CounterOpts) prometheus.Counter {
	opts.Namespace = r.namespace
	opts.Subsystem = r.subsystem
	return promauto.NewCounter(opts)
}

// NewGauge creates a new gauge with proper naming.
func (r *ComponentRegistry) NewGauge(opts prometheus.GaugeOpts) prometheus.Gauge {
	opts.Namespace = r.namespace
	opts.Subsystem = r.subsystem
	return promauto.NewGauge(opts)
}

// NewGaugeVec creates a new gauge vector with proper naming.
func (r *ComponentRegistry) NewGaugeVec(opts prometheus.GaugeOpts, labelNames []string) *prometheus.GaugeVec {
	opts.Namespace = r.namespace
	opts.Subsystem = r.subsystem
	return promauto.NewGaugeVec(opts, labelNames)
}

// NewHistogram creates a new histogram with proper naming.
func (r *ComponentRegistry) NewHistogram(opts prometheus.HistogramOpts) prometheus.Histogram {
	opts.Namespace = r.namespace
	opts.Subsystem = r.subsystem
	return promauto.NewHistogram(opts)
}

// NewHistogramVec creates a new histogram vector with proper naming.
func (r *ComponentRegistry) NewHistogramVec(opts prometheus.HistogramOpts, labelNames []string,
) *prometheus.HistogramVec {
	opts.Namespace = r.namespace
	opts.Subsystem = r.subsystem
	return promauto.NewHistogramVec(opts, labelNames)
}
