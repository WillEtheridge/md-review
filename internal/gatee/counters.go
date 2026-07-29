// Package gatee provides opt-in, process-local measurement counters for the
// Gate E baseline harness. Production code leaves the counter dependency nil.
package gatee

import "sync/atomic"

const (
	// EnvironmentVariable is the exact opt-in used by the compiled-binary Gate E
	// harness. Ordinary mdReview processes do not expose measurement counters.
	EnvironmentVariable = "MDREVIEW_GATE_E_COUNTERS"
)

// Snapshot is one monotonic measurement-counter observation. Active asset
// streams is a gauge; all other fields are process-lifetime totals or maxima.
type Snapshot struct {
	CompleteWorkspaceScans uint64 `json:"completeWorkspaceScans"`
	MarkdownContentOpens   uint64 `json:"markdownContentOpens"`
	MarkdownContentBytes   uint64 `json:"markdownContentBytes"`
	SidecarContentOpens    uint64 `json:"sidecarContentOpens"`
	SidecarContentBytes    uint64 `json:"sidecarContentBytes"`
	GitignoreContentOpens  uint64 `json:"gitignoreContentOpens"`
	GitignoreContentBytes  uint64 `json:"gitignoreContentBytes"`
	ActiveAssetStreams     int64  `json:"activeAssetStreams"`
	MaximumAssetStreams    int64  `json:"maximumActiveAssetStreams"`
	AssetStreamBytes       uint64 `json:"assetStreamBytes"`
}

// Counters owns lock-free measurement state shared by the workspace, review
// store, and HTTP server of one explicitly instrumented process.
type Counters struct {
	completeWorkspaceScans atomic.Uint64
	markdownContentOpens   atomic.Uint64
	markdownContentBytes   atomic.Uint64
	sidecarContentOpens    atomic.Uint64
	sidecarContentBytes    atomic.Uint64
	gitignoreContentOpens  atomic.Uint64
	gitignoreContentBytes  atomic.Uint64
	activeAssetStreams     atomic.Int64
	maximumAssetStreams    atomic.Int64
	assetStreamBytes       atomic.Uint64
}

// RecordCompleteWorkspaceScan records one successfully completed descriptor-
// relative scan. Failed or cancelled scan attempts are deliberately excluded.
func (counters *Counters) RecordCompleteWorkspaceScan() {
	if counters != nil {
		counters.completeWorkspaceScans.Add(1)
	}
}

// RecordMarkdownContentRead records one Markdown content-open attempt and the
// bytes returned by that open. Metadata-only scans never call this method.
func (counters *Counters) RecordMarkdownContentRead(sizeBytes int) {
	if counters == nil {
		return
	}
	counters.markdownContentOpens.Add(1)
	addNonNegative(counters.markdownContentBytes.Add, sizeBytes)
}

// RecordSidecarContentRead records one sidecar content-open attempt and the
// bytes returned by that open. A missing sidecar therefore records an open and
// zero bytes.
func (counters *Counters) RecordSidecarContentRead(sizeBytes int) {
	if counters == nil {
		return
	}
	counters.sidecarContentOpens.Add(1)
	addNonNegative(counters.sidecarContentBytes.Add, sizeBytes)
}

// RecordGitignoreContentRead records one .gitignore content-open attempt and
// the bytes returned by that open. Signature-cache hits do not call it.
func (counters *Counters) RecordGitignoreContentRead(sizeBytes int) {
	if counters == nil {
		return
	}
	counters.gitignoreContentOpens.Add(1)
	addNonNegative(counters.gitignoreContentBytes.Add, sizeBytes)
}

// BeginAssetStream records a stream after it acquires the process semaphore.
// The returned function must be called exactly once on every exit path.
func (counters *Counters) BeginAssetStream() func() {
	if counters == nil {
		return func() {}
	}
	current := counters.activeAssetStreams.Add(1)
	for {
		maximum := counters.maximumAssetStreams.Load()
		if current <= maximum || counters.maximumAssetStreams.CompareAndSwap(maximum, current) {
			break
		}
	}
	return func() {
		counters.activeAssetStreams.Add(-1)
	}
}

// RecordAssetStreamBytes adds bytes read once from the contained asset
// descriptor. Replayed type-detection prefixes must not be recorded again.
func (counters *Counters) RecordAssetStreamBytes(sizeBytes int) {
	if counters == nil {
		return
	}
	addNonNegative(counters.assetStreamBytes.Add, sizeBytes)
}

// Snapshot returns one race-safe observation without resetting monotonic
// totals. Measurement harnesses derive case-local values with before/after
// deltas.
func (counters *Counters) Snapshot() Snapshot {
	if counters == nil {
		return Snapshot{}
	}
	return Snapshot{
		CompleteWorkspaceScans: counters.completeWorkspaceScans.Load(),
		MarkdownContentOpens:   counters.markdownContentOpens.Load(),
		MarkdownContentBytes:   counters.markdownContentBytes.Load(),
		SidecarContentOpens:    counters.sidecarContentOpens.Load(),
		SidecarContentBytes:    counters.sidecarContentBytes.Load(),
		GitignoreContentOpens:  counters.gitignoreContentOpens.Load(),
		GitignoreContentBytes:  counters.gitignoreContentBytes.Load(),
		ActiveAssetStreams:     counters.activeAssetStreams.Load(),
		MaximumAssetStreams:    counters.maximumAssetStreams.Load(),
		AssetStreamBytes:       counters.assetStreamBytes.Load(),
	}
}

func addNonNegative(add func(uint64) uint64, sizeBytes int) {
	if sizeBytes > 0 {
		add(uint64(sizeBytes))
	}
}
