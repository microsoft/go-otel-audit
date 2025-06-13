package audit

import (
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

type metrics struct {
	meter        metric.Meter
	msgsSent     metric.Int64Counter
	msgsRequeued metric.Int64Counter
	msgsDropped  metric.Int64Counter
	msgErrs      metric.Int64Counter

	// requeuedCounter exists to track the number of requeued messages in a test.
	// You cannot extract the value from an otel counter.
	requeuedCounter atomic.Uint64
}

func newMetrics() *metrics {
	m := &metrics{
		meter: otel.GetMeterProvider().Meter("github.com/microsoft/go-otel-audit/audit"),
	}
	m.msgsSent, _ = m.meter.Int64Counter("messages_sent")
	m.msgsRequeued, _ = m.meter.Int64Counter("messages_requeued")
	m.msgsDropped, _ = m.meter.Int64Counter("messages_dropped")
	m.msgErrs, _ = m.meter.Int64Counter("messages_errors")
	return m
}
