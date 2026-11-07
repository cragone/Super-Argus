package query

import (
	"context"
	"sort"
	"time"

	"github.com/cragone/Super-Argus/internal/models"
)

// ServiceActiveWindow is how recent a stream's last_seen must be for its
// service to count as active.
const ServiceActiveWindow = time.Minute

// ServiceInfo summarises every stream that carries one value of the service label.
type ServiceInfo struct {
	Name      string    `json:"name"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Active    bool      `json:"active"`
	Streams   int       `json:"streams"`
	Lines     int64     `json:"lines"`
	Bytes     int64     `json:"bytes"`
}

// Services lists the values of the label `key` as services, with activity and
// volume. start/end are nanoseconds (0 = unbounded) and bound which streams and
// chunks count. Last-seen lags by at most one engine tick, and volume only
// counts flushed chunks, so very recent lines may not be reflected in Lines.
func (s *Service) Services(ctx context.Context, key string, start, end int64, now time.Time) ([]ServiceInfo, error) {
	if key == "" {
		return nil, badf("service label key is required")
	}
	type streamRow struct {
		ID        uint64
		Value     string
		FirstSeen time.Time
		LastSeen  time.Time
	}
	q := s.DB.WithContext(ctx).Model(&models.StreamLabel{}).
		Select("stream_labels.stream_id AS id, stream_labels.value AS value, streams.first_seen AS first_seen, streams.last_seen AS last_seen").
		Joins("JOIN streams ON streams.id = stream_labels.stream_id").
		Where("stream_labels.key = ?", key)
	if start > 0 {
		q = q.Where("streams.last_seen >= ?", time.Unix(0, start))
	}
	if end > 0 {
		q = q.Where("streams.first_seen <= ?", time.Unix(0, end))
	}
	var rows []streamRow
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}

	svcByStream := make(map[uint64]string, len(rows))
	byName := map[string]*ServiceInfo{}
	for _, r := range rows {
		svcByStream[r.ID] = r.Value
		info := byName[r.Value]
		if info == nil {
			info = &ServiceInfo{Name: r.Value, FirstSeen: r.FirstSeen, LastSeen: r.LastSeen}
			byName[r.Value] = info
		}
		info.Streams++
		if r.FirstSeen.Before(info.FirstSeen) {
			info.FirstSeen = r.FirstSeen
		}
		if r.LastSeen.After(info.LastSeen) {
			info.LastSeen = r.LastSeen
		}
	}

	if len(svcByStream) > 0 {
		type chunkRow struct {
			StreamID uint64
			Lines    int64
			Bytes    int64
		}
		ids := make([]uint64, 0, len(svcByStream))
		for id := range svcByStream {
			ids = append(ids, id)
		}
		cq := s.DB.WithContext(ctx).Model(&models.Chunk{}).
			Select("stream_id, SUM(line_count) AS lines, SUM(bytes_raw) AS bytes").
			Where("stream_id IN ?", ids).
			Group("stream_id")
		if start > 0 {
			cq = cq.Where("max_ts >= ?", start)
		}
		if end > 0 {
			cq = cq.Where("min_ts <= ?", end)
		}
		var chunks []chunkRow
		if err := cq.Scan(&chunks).Error; err != nil {
			return nil, err
		}
		for _, c := range chunks {
			if info := byName[svcByStream[c.StreamID]]; info != nil {
				info.Lines += c.Lines
				info.Bytes += c.Bytes
			}
		}
	}

	out := make([]ServiceInfo, 0, len(byName))
	for _, info := range byName {
		info.Active = now.Sub(info.LastSeen) <= ServiceActiveWindow
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
