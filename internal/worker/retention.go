package worker

import (
	"errors"
	"sort"
	"time"
)

var errStoreCapacity = errors.New("active record capacity reached")

const (
	storeRecordLimit = 128
	storeRetention   = time.Hour
)

type finishedRecord struct {
	id string
	at time.Time
}

// Only ended processes are eligible; active records must never be evicted.
func expiredRecords(records []finishedRecord, total, limit int, now time.Time) []string {
	sort.Slice(records, func(i, j int) bool { return records[i].at.Before(records[j].at) })
	var ids []string
	for _, record := range records {
		if total <= limit && now.Sub(record.at) < storeRetention {
			break
		}
		ids = append(ids, record.id)
		total--
	}
	return ids
}
