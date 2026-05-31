package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// eventsDroppedTotal tracks events dropped due to Valkey failures or fallback capacity overflows.
	eventsDroppedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "limen_billing_events_dropped_total",
		Help: "Total number of billing events dropped due to Valkey failures or fallback capacity overflows.",
	})

	// streamEvictedTotal tracks messages that had to be dropped or evicted, including DLQ moves.
	streamEvictedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "limen_billing_stream_evicted_total",
		Help: "Total number of billing stream messages evicted or moved to dead-letter queue.",
	})
)
