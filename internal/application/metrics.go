package application

import "time"

// MetricsRecorder is the slim port the use cases use to publish
// telemetry. Production wires *infrastructure/metrics.Metrics; tests
// pass nil and the use cases skip the emit. Defined here (not in
// internal/ports) because metrics is an infrastructure concern, not
// a domain port — but use cases produce the events, so they own
// the contract they need.
type MetricsRecorder interface {
	NotificationCreated(channel, priority string)
	NotificationDelivered(channel string)
	NotificationFailed(channel, reason string)
	NotificationAttempt(channel, outcome string)
	ObserveProcessing(channel string, d time.Duration)
}
