// Package collector implements prometheus.Collector for each data source.
// Collectors never call the Scaleway API: they only read the last snapshot
// published by a refresher and emit constant metrics from it.
package collector

// Snapshotter returns the last published snapshot, or nil if there is none yet.
// Implementations must be safe for concurrent use.
type Snapshotter[T any] interface {
	Snapshot() *T
}
