// Package telemetry provides a centralized system for application observability.
// It integrates structured logging, Prometheus metrics, and OpenTelemetry tracing
// to give deep insights into the Open Outbox Relay's operation.
//
// The core components of this package include:
//   - Telemetry: A composite struct that bundles a `zap.Logger`, `Metrics` (Prometheus),
//     and OpenTelemetry `Tracer` and `Meter` instances. This allows for easy
//     injection of all observability tools into any component.
//   - Metrics: Manages Prometheus metric instruments (e.g., counters, gauges, histograms)
//     for tracking key performance indicators and operational health.
//   - OTelProviders: Handles the setup and shutdown of OpenTelemetry SDKs, including
//     configuring exporters for traces and metrics.
//
// This package aims to make it simple for other parts of the application to
// emit logs, record metrics, and create spans without needing to manage the
// underlying observability SDKs directly. It promotes consistent instrumentation
// across the entire relay.
package telemetry

// InstrumentationName is the unique identifier used for OpenTelemetry tracing and metrics
// associated with the relay's internal operations.
const InstrumentationName = "github.com/open-outbox/relay"
