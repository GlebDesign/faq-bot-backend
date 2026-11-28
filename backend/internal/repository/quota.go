package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/service"

	"github.com/jackc/pgx/v5/pgtype"
)

type Quota struct{ pool *sql.DB }

func NewQuota(pool *sql.DB) *Quota { return &Quota{pool: pool} }

const availableAllocationsSQL = `
	SELECT a.id, a.kind, a.granted_at, c.as_of,
		   GREATEST(a.request_count - COALESCE(held.total, 0), 0) AS available
	FROM quota_allocations a
	LEFT JOIN (
		SELECT allocation_id, count(*) AS total
		FROM quota_reservations
		WHERE account_id=$1 AND state IN ('active', 'consumed')
		GROUP BY allocation_id
	) held ON held.allocation_id=a.id
	CROSS JOIN quota_clock c
	WHERE a.account_id=$1 AND a.granted_at <= c.as_of
	  AND (a.expires_at IS NULL OR a.expires_at > c.as_of)`

const quotaAvailableSQL = `
	WITH quota_clock AS (SELECT statement_timestamp() AS as_of)
	SELECT COALESCE(SUM(available),0)::bigint FROM (` + availableAllocationsSQL + `) a`

func (q *Quota) Available(ctx context.Context, accountID string) (int64, error) {
	var available int64
	err := q.pool.QueryRowContext(ctx, quotaAvailableSQL, accountID).Scan(&available)
	return available, db.WrapError("read available quota", err)
}

func lockQuotaAccount(ctx context.Context, tx *sql.Tx, accountID string) error {
	_, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('quota:' || $1::uuid::text,0))", accountID)
	return db.WrapError("lock company quota", err)
}

func (q *Quota) write(ctx context.Context, accountID string, fn func(context.Context, *sql.Tx) error) error {
	return db.WithTx(ctx, q.pool, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "SET TRANSACTION ISOLATION LEVEL READ COMMITTED"); err != nil {
			return db.WrapError("set quota isolation", err)
		}
		if err := lockQuotaAccount(ctx, tx, accountID); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

func (q *Quota) Reserve(ctx context.Context, accountID, requestID string, timeout time.Duration) (time.Time, error) {
	if timeout < time.Microsecond {
		return time.Time{}, service.ErrInvalidGeneration
	}
	var deadline time.Time
	err := q.write(ctx, accountID, func(ctx context.Context, tx *sql.Tx) error {
		var state string
		var reserved bool
		err := tx.QueryRowContext(ctx, `
			SELECT r.state, EXISTS (SELECT 1 FROM quota_reservations q WHERE q.account_id=r.account_id AND q.ai_request_id=r.id)
			FROM ai_requests r WHERE r.account_id=$1 AND r.id=$2`, accountID, requestID).Scan(&state, &reserved)
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrQuotaRequestNotFound
		}
		if err != nil {
			return db.WrapError("read request for quota", err)
		}
		if reserved {
			return service.ErrQuotaAlreadyReserved
		}
		if state != "accepted" {
			return service.ErrQuotaRequestNotPending
		}
		err = tx.QueryRowContext(ctx, `
			WITH quota_clock AS MATERIALIZED (SELECT clock_timestamp() AS as_of)
			INSERT INTO quota_reservations (account_id,ai_request_id,allocation_id,reserved_at,deadline_at)
			SELECT $1,$2,a.id,a.as_of,a.as_of+$3::interval FROM (`+availableAllocationsSQL+`) a WHERE a.available>0
			ORDER BY (a.kind='trial') DESC,a.granted_at,a.id LIMIT 1
			RETURNING deadline_at`, accountID, requestID, pgtype.Interval{Microseconds: timeout.Microseconds(), Valid: true}).Scan(&deadline)
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrQuotaExhausted
		}
		return db.WrapError("create quota reservation", err)
	})
	if err != nil {
		return time.Time{}, err
	}
	return deadline, nil
}
