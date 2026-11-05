package query

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/cragone/Super-Argus/internal/models"
	"gorm.io/gorm"
)

func decodeLabels(s string) (map[string]string, error) {
	m := map[string]string{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// Labels returns the label names seen in [start, end] (nanoseconds; 0 = unbounded).
func (s *Service) Labels(ctx context.Context, start, end int64) ([]string, error) {
	var names []string
	err := s.labelQuery(ctx, start, end).Distinct().Order("stream_labels.key").Pluck("stream_labels.key", &names).Error
	return names, err
}

// LabelValues returns the values of one label seen in [start, end].
func (s *Service) LabelValues(ctx context.Context, name string, start, end int64) ([]string, error) {
	if name == "" {
		return nil, badf("label name is required")
	}
	var vals []string
	err := s.labelQuery(ctx, start, end).Where("stream_labels.key = ?", name).Distinct().Pluck("stream_labels.value", &vals).Error
	sort.Strings(vals)
	return vals, err
}

// labelQuery selects stream_labels of streams active in the range. A stream's
// last_seen lags by at most one engine tick, so it is a safe activity bound.
func (s *Service) labelQuery(ctx context.Context, start, end int64) *gorm.DB {
	q := s.DB.WithContext(ctx).Model(&models.StreamLabel{})
	if start > 0 || end > 0 {
		q = q.Joins("JOIN streams ON streams.id = stream_labels.stream_id")
		if start > 0 {
			q = q.Where("streams.last_seen >= ?", time.Unix(0, start))
		}
		if end > 0 {
			q = q.Where("streams.first_seen <= ?", time.Unix(0, end))
		}
	}
	return q
}
