package consensus

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/ethereum/go-ethereum/internal/sp/metrics"
)

// Metrics holds all consensus-level metrics.
type Metrics struct {
	registry *metrics.ComponentRegistry

	TransactionsTotal          *prometheus.CounterVec
	ActiveTransactions         prometheus.Gauge
	Duration                   *prometheus.HistogramVec
	VotesReceived              *prometheus.CounterVec
	VoteLatency                *prometheus.HistogramVec
	Timeouts                   prometheus.Counter
	ParticipantsPerTransaction prometheus.Histogram
	DecisionsBroadcast         *prometheus.CounterVec
	VoteBroadcast              *prometheus.CounterVec
}

// NewMetrics creates consensus metrics.
func NewMetrics() *Metrics {
	reg := metrics.NewComponentRegistry("publisher", "consensus")

	return &Metrics{
		registry: reg,

		TransactionsTotal: reg.NewCounterVec(prometheus.CounterOpts{
			Name: "transactions_total",
			Help: "Total number of consensus transactions",
		}, []string{"state"}),

		ActiveTransactions: reg.NewGauge(prometheus.GaugeOpts{
			Name: "active_transactions",
			Help: "Number of active consensus transactions",
		}),

		Duration: reg.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "duration_seconds",
			Help:    "Duration of consensus transactions",
			Buckets: metrics.ConsensusBuckets,
		}, []string{"state"}),

		VotesReceived: reg.NewCounterVec(prometheus.CounterOpts{
			Name: "votes_received_total",
			Help: "Total number of votes received",
		}, []string{"chain_id", "vote"}),

		VoteLatency: reg.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "vote_latency_seconds",
			Help:    "Latency from transaction start to vote received",
			Buckets: metrics.ConsensusBuckets,
		}, []string{"chain_id"}),

		Timeouts: reg.NewCounter(prometheus.CounterOpts{
			Name: "timeouts_total",
			Help: "Total number of transaction timeouts",
		}),

		ParticipantsPerTransaction: reg.NewHistogram(prometheus.HistogramOpts{
			Name:    "participants_per_transaction",
			Help:    "Number of participants per transaction",
			Buckets: metrics.CountBuckets,
		}),

		DecisionsBroadcast: reg.NewCounterVec(prometheus.CounterOpts{
			Name: "decisions_broadcast_total",
			Help: "Total number of decisions broadcast",
		}, []string{"decision"}),

		VoteBroadcast: reg.NewCounterVec(prometheus.CounterOpts{
			Name: "vote_broadcast_total",
			Help: "Total number of votes broadcast",
		}, []string{"vote"}),
	}
}

// RecordTransactionStarted records a transaction start.
func (m *Metrics) RecordTransactionStarted(participantCount int) {
	m.TransactionsTotal.WithLabelValues("initiated").Inc()
	m.ActiveTransactions.Inc()
	m.ParticipantsPerTransaction.Observe(float64(participantCount))
}

// RecordTransactionCompleted records a transaction completion.
func (m *Metrics) RecordTransactionCompleted(state string, duration time.Duration) {
	m.TransactionsTotal.WithLabelValues(state).Inc()
	m.Duration.WithLabelValues(state).Observe(duration.Seconds())
	m.ActiveTransactions.Dec()
}

// RecordVote records a vote received from a sequencer.
func (m *Metrics) RecordVote(chainID string, vote bool, latency time.Duration) {
	state := StateCommit
	if !vote {
		state = StateAbort
	}

	m.VotesReceived.WithLabelValues(chainID, state.String()).Inc()
	m.VoteLatency.WithLabelValues(chainID).Observe(latency.Seconds())
}

// RecordTimeout records a transaction timeout.
func (m *Metrics) RecordTimeout() {
	m.Timeouts.Inc()
	m.ActiveTransactions.Dec()
}

// RecordDecisionBroadcast records a decision broadcast.
func (m *Metrics) RecordDecisionBroadcast(decision bool) {
	state := StateCommit
	if !decision {
		state = StateAbort
	}

	m.DecisionsBroadcast.WithLabelValues(state.String()).Inc()
}

// RecordDecisionBroadcast records a decision broadcast.
func (m *Metrics) RecordVoteBroadcast(vote bool) {
	state := StateCommit
	if !vote {
		state = StateAbort
	}

	m.VoteBroadcast.WithLabelValues(state.String()).Inc()
}
