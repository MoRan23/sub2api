package service

// Selection is per credential owner and final model. A configuration change
// resets persisted selection; this fallback also supports pre-migration rows.
func codexTurnStateSelectedProxy(ids []int64, current int64) int64 {
	for _, id := range ids {
		if id == current {
			return current
		}
	}
	if len(ids) != 0 {
		return ids[0]
	}
	return 0
}

func codexTurnStateProxyAllowed(ids []int64, id int64) bool {
	if id <= 0 {
		return false
	}
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func rotateCodexTurnStateProxy(record *CodexTurnStateRecord, ids []int64) {
	record.CollectorExtendedCount++
	if record.CollectorExtendedCount < 3 {
		return
	}
	record.CollectorExtendedCount = 0
	for i, id := range ids {
		if id == record.CollectorProxyID {
			record.CollectorProxyID = ids[(i+1)%len(ids)]
			return
		}
	}
	record.CollectorProxyID = codexTurnStateSelectedProxy(ids, 0)
}

func codexStateProxyIDPtr(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}
