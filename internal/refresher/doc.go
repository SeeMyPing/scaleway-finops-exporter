// Package refresher periodically fetches data in the background and publishes
// it as an immutable snapshot, so that Prometheus scrapes never call the
// Scaleway API.
package refresher
