package wtc

import (
	"context"
	"time"
)

func statusResourceWriteSince(ctx context.Context, kind, forge, slug, key string, raw []byte, started time.Time) bool {
	publication, _ := statusWriteForgeEntrySinceObservedContext(ctx, kind, forge, slug, key, raw, started, time.Time{})
	return publication.Written
}

func statusResourceRetainedMerge(ctx context.Context, record PRRecord, slug, forge string) (statusPRDetail, bool) {
	detail, known, _ := statusRetainedMergeContext(ctx, record, slug, forge)
	return detail, known
}

func statusResourceObservedDetail(ctx context.Context, record PRRecord, slug, forge string, age time.Duration) (statusPRDetail, bool) {
	detail, known, _ := statusReadObservedDetailContext(ctx, record, slug, forge, age)
	return detail, known
}

func statusResourceSettledMerge(ctx context.Context, record PRRecord, slug, forge string) (statusPRDetail, bool) {
	detail, known := statusResourceRetainedMerge(ctx, record, slug, forge)
	if !known || detail.MergedOn == "" || detail.ChecksUnsettled || detail.Checks == "PENDING" {
		return statusPRDetail{}, false
	}
	detail.Freshness, detail.StateSource = "settled", "settled-cache"
	return detail, true
}

// Markers retain their released format and write path. A cancellation check
// precedes publication; filesystem I/O already in progress is not cancellable.
func statusResourceWriteEntry(ctx context.Context, kind, forge, slug, key string, raw []byte) bool {
	if ctx.Err() != nil {
		return false
	}
	return statusWriteForgeEntryObserved(kind, forge, slug, key, raw, time.Time{})
}

func statusResourceUnavailableDetail(ctx context.Context, record PRRecord, slug, forge string) statusPRDetail {
	if known, ok := statusResourceRetainedMerge(ctx, record, slug, forge); ok {
		known.Freshness = "unavailable"
		return known
	}
	return statusDeferredDetail(record)
}
