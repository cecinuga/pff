package warehouse

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/cecinuga/pff/ms/internal/domain"
)

// SyncCatalog upserts the metric domains and classes of domain.Catalog into
// their dimension tables. SCD type 1: labels and order are overwritten in
// place. Rows are never deleted, because facts reference them.
func (s *Store) SyncCatalog(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for i, d := range domain.Catalog {
			if _, err := tx.Exec(ctx, `
				INSERT INTO dim_metric_domain (metric_domain, label, description, sort_order) VALUES ($1, $2, $3, $4)
				ON CONFLICT (metric_domain) DO UPDATE SET label = EXCLUDED.label, description = EXCLUDED.description,
					sort_order = EXCLUDED.sort_order, updated_at = now()`,
				string(d.Domain), d.Label, d.Description, i); err != nil {
				return err
			}
			for j, c := range d.Classes {
				if _, err := tx.Exec(ctx, `
					INSERT INTO dim_metric_class (metric_domain, metric_class, label, sort_order) VALUES ($1, $2, $3, $4)
					ON CONFLICT (metric_domain, metric_class) DO UPDATE SET label = EXCLUDED.label,
						sort_order = EXCLUDED.sort_order, updated_at = now()`,
					string(d.Domain), string(c.Class), c.Label, j); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
