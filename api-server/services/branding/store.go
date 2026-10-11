package branding

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"nudgebee/services/internal/database"

	"github.com/jmoiron/sqlx"
)

func db() (*sqlx.DB, error) {
	manager, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		return nil, err
	}
	return manager.Db, nil
}

// scanRow is the wire shape straight out of Postgres; jsonb columns arrive as
// []byte and are decoded into the map fields of Row.
type scanRow struct {
	Id                string         `db:"id"`
	Host              string         `db:"host"`
	Overrides         []byte         `db:"overrides"`
	PreviousOverrides []byte         `db:"previous_overrides"`
	Enabled           bool           `db:"enabled"`
	CreatedAt         string         `db:"created_at"`
	UpdatedAt         string         `db:"updated_at"`
	UpdatedBy         sql.NullString `db:"updated_by"`
}

func (s scanRow) toRow() (Row, error) {
	r := Row{
		Id:        s.Id,
		Host:      s.Host,
		Enabled:   s.Enabled,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
		UpdatedBy: s.UpdatedBy.String,
	}
	if len(s.Overrides) > 0 {
		if err := json.Unmarshal(s.Overrides, &r.Overrides); err != nil {
			return Row{}, fmt.Errorf("decoding overrides for host %q: %w", s.Host, err)
		}
	}
	if len(s.PreviousOverrides) > 0 {
		if err := json.Unmarshal(s.PreviousOverrides, &r.PreviousOverrides); err != nil {
			return Row{}, fmt.Errorf("decoding previous_overrides for host %q: %w", s.Host, err)
		}
	}
	return r, nil
}

const rowCols = `id::text AS id, host, overrides, previous_overrides, enabled,
	created_at::text AS created_at, updated_at::text AS updated_at, updated_by::text AS updated_by`

// listRows returns every override. The table holds one row per partner
// hostname — a handful — so the frontend pulls the whole set in one call and
// resolves per-host in memory rather than querying on each request.
func listRows(d *sqlx.DB, enabledOnly bool) ([]Row, error) {
	q := `SELECT ` + rowCols + ` FROM tenant_branding_override`
	if enabledOnly {
		q += ` WHERE enabled = true`
	}
	q += ` ORDER BY host`

	// sqlclosecheck: the rows handle must be closed by a deferred call, not an
	// inline one, so the scan loop lives in its own function.
	var scanned []scanRow
	if err := d.Select(&scanned, q); err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(scanned))
	for _, s := range scanned {
		r, err := s.toRow()
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func getRow(d *sqlx.DB, host string) (*Row, error) {
	var s scanRow
	err := d.Get(&s, `SELECT `+rowCols+` FROM tenant_branding_override WHERE host = $1`, host)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r, err := s.toRow()
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// upsertRow writes the override and rolls the outgoing value into
// previous_overrides in the same statement, so one-step undo never races with
// a concurrent write.
func upsertRow(d *sqlx.DB, host string, overrides []byte, enabled bool, actorId string) (string, error) {
	var id string
	err := d.QueryRowx(`
		INSERT INTO tenant_branding_override (host, overrides, enabled, created_by, updated_by)
		VALUES ($1, $2::jsonb, $3, NULLIF($4,'')::uuid, NULLIF($4,'')::uuid)
		ON CONFLICT ON CONSTRAINT tenant_branding_override_host_uniq
		DO UPDATE SET
			previous_overrides = tenant_branding_override.overrides,
			overrides          = EXCLUDED.overrides,
			enabled            = EXCLUDED.enabled,
			updated_by         = EXCLUDED.updated_by,
			updated_at         = now()
		RETURNING id::text`,
		host, string(overrides), enabled, actorId,
	).Scan(&id)
	return id, err
}

// restoreRow swaps overrides and previous_overrides. Swapping rather than
// overwriting keeps undo reversible — a mis-aimed restore is itself undoable.
func restoreRow(d *sqlx.DB, host, actorId string) (int64, error) {
	res, err := d.Exec(`
		UPDATE tenant_branding_override
		SET overrides          = previous_overrides,
		    previous_overrides = overrides,
		    updated_by         = NULLIF($2,'')::uuid,
		    updated_at         = now()
		WHERE host = $1 AND previous_overrides IS NOT NULL`,
		host, actorId)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func deleteRow(d *sqlx.DB, host string) (int64, error) {
	res, err := d.Exec(`DELETE FROM tenant_branding_override WHERE host = $1`, host)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
