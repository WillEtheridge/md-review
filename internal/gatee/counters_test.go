package gatee

import (
	"sync"
	"testing"
)

func TestCountersRecordContentAndCompletedScans(t *testing.T) {
	counters := &Counters{}
	counters.RecordCompleteWorkspaceScan()
	counters.RecordMarkdownContentRead(12)
	counters.RecordMarkdownContentRead(-1)
	counters.RecordSidecarContentRead(7)
	counters.RecordGitignoreContentRead(3)

	got := counters.Snapshot()
	if got.CompleteWorkspaceScans != 1 ||
		got.MarkdownContentOpens != 2 ||
		got.MarkdownContentBytes != 12 ||
		got.SidecarContentOpens != 1 ||
		got.SidecarContentBytes != 7 ||
		got.GitignoreContentOpens != 1 ||
		got.GitignoreContentBytes != 3 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestCountersTrackConcurrentAssetGaugeAndBytes(t *testing.T) {
	counters := &Counters{}
	const streamCount = 8
	started := make(chan struct{}, streamCount)
	release := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(streamCount)

	for range streamCount {
		go func() {
			defer wait.Done()
			finish := counters.BeginAssetStream()
			defer finish()
			counters.RecordAssetStreamBytes(4)
			started <- struct{}{}
			<-release
		}()
	}
	for range streamCount {
		<-started
	}
	observed := counters.Snapshot()
	if observed.ActiveAssetStreams != streamCount ||
		observed.MaximumAssetStreams != streamCount ||
		observed.AssetStreamBytes != 4*streamCount {
		t.Fatalf("active snapshot = %+v", observed)
	}
	close(release)
	wait.Wait()

	observed = counters.Snapshot()
	if observed.ActiveAssetStreams != 0 || observed.MaximumAssetStreams != streamCount {
		t.Fatalf("completed snapshot = %+v", observed)
	}
}
