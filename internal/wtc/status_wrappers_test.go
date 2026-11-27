package wtc

import "context"

// Background-intent shorthands for tests; production code passes its own
// context and intent.

func statusEnrichRecord(record PRRecord, slug, forge string) statusPRDetail {
	return statusEnrichRecordContext(context.Background(), StatusRefreshBackground, record, slug, forge)
}

func statusEnrichRecordMetadata(record PRRecord, slug, forge string) statusPRDetail {
	return statusEnrichRecordMetadataContext(context.Background(), StatusRefreshBackground, record, slug, forge)
}

func statusEnrichListed(record PRRecord, slug, forge string, item statusOpenPR) statusPRDetail {
	return statusEnrichListedContext(context.Background(), record, slug, forge, item, false)
}

func (c *Context) statusEnrichRecords(records []PRRecord, rows []StatusRepo) ([]statusPRDetail, error) {
	return c.statusEnrichRecordsWithIntent(context.Background(), StatusRefreshBackground, records, rows)
}

func statusOpenPRs(forge, slug string) (map[string]statusOpenPR, bool, error) {
	return statusOpenPRsContext(context.Background(), forge, slug)
}

func statusFetchBBOpenList(slug string) ([]byte, error) {
	return statusFetchBBOpenListContext(context.Background(), StatusRefreshBackground, slug)
}
