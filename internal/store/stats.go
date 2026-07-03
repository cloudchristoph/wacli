package store

type StoreStats struct {
	Messages      int64
	Chats         int64
	Contacts      int64
	Groups        int64
	LastMessageTS int64
}

func (d *DB) Stats() (StoreStats, error) {
	row, err := d.q.Stats(storeCtx())
	if err != nil {
		return StoreStats{}, err
	}
	return StoreStats{
		Messages:      row.Count,
		Chats:         row.Count_2,
		Contacts:      row.Count_3,
		Groups:        row.Count_4,
		LastMessageTS: sqlInt64(row.Coalesce),
	}, nil
}
