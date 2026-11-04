package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/models"
	"faqbot/backend/internal/service"
)

type Auth struct{ pool *sql.DB }

func NewAuth(pool *sql.DB) *Auth { return &Auth{pool: pool} }

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func (a *Auth) Bootstrap(ctx context.Context, subject string) (models.Identity, error) {
	var identity models.Identity
	if subject == "" {
		return identity, service.ErrBadGateway
	}
	err := db.WithTx(ctx, a.pool, func(ctx context.Context, tx *sql.Tx) error {
		userID := uuid()
		var inserted string
		err := tx.QueryRowContext(ctx,
			`INSERT INTO users(id,auth_subject) VALUES($1,$2)
					ON CONFLICT(auth_subject) DO NOTHING RETURNING id`, userID, subject).Scan(&inserted)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return db.WrapError("create identity user", err)
		}
		if err == nil {
			accountID := uuid()
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO accounts(id) VALUES($1)`, accountID); err != nil {
				return db.WrapError("create identity account", err)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO account_members(user_id,account_id,role)
						VALUES($1,$2,'owner')`, userID, accountID); err != nil {
				return db.WrapError("create owner membership", err)
			}
		}
		err = tx.QueryRowContext(ctx, `SELECT u.id,m.account_id,m.role,m.audit_read FROM users u JOIN account_members m ON m.user_id=u.id WHERE u.auth_subject=$1 ORDER BY m.account_id LIMIT 1`, subject).Scan(&identity.UserID, &identity.AccountID, &identity.Role, &identity.AuditRead)
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrUnauthorized
		}
		return db.WrapError("read identity membership", err)
	})
	return identity, err
}

func (a *Auth) CreateSession(ctx context.Context, hash []byte, csrf string, identity *models.Identity, now, expiry time.Time) error {
	if err := a.CleanupSessions(ctx, now); err != nil {
		return err
	}
	var userID, accountID any
	idle := expiry
	if identity != nil {
		userID = identity.UserID
		accountID = identity.AccountID
		idle = now.Add(30 * time.Minute)
	}
	_, err := a.pool.ExecContext(ctx,
		`INSERT INTO sessions(token_hash,csrf,user_id,account_id,created_at,expires_at,idle_expires_at)
				VALUES($1,$2,$3,$4,$5,$6,$7)`, hash, csrf, userID, accountID, now, expiry, idle)
	return db.WrapError("create session", err)
}

func (a *Auth) GetSession(ctx context.Context, hash []byte, now time.Time) (models.Session, error) {
	var s models.Session
	err := db.WithTx(ctx, a.pool, func(ctx context.Context, tx *sql.Tx) error {
		var userID, accountID sql.NullString
		err := tx.QueryRowContext(ctx,
			`UPDATE sessions
					SET idle_expires_at=LEAST(expires_at,$2::timestamptz+interval '30 minutes')
					WHERE token_hash=$1 AND revoked_at IS NULL
					  AND expires_at>$2 AND idle_expires_at>$2 RETURNING csrf,user_id,account_id,expires_at`,
			hash, now).Scan(&s.CSRF, &userID, &accountID, &s.ExpiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrUnauthorized
		}
		if err != nil {
			return db.WrapError("read session", err)
		}
		if !userID.Valid {
			return nil
		}
		identity := &models.Identity{UserID: userID.String, AccountID: accountID.String}
		err = tx.QueryRowContext(ctx,
			`SELECT role,audit_read
					FROM account_members
					WHERE user_id=$1 AND account_id=$2`,
			identity.UserID, identity.AccountID).Scan(&identity.Role, &identity.AuditRead)
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrUnauthorized
		}
		if err != nil {
			return db.WrapError("read session membership", err)
		}
		s.Identity = identity
		return nil
	})
	return s, err
}

func (a *Auth) RevokeSession(ctx context.Context, hash []byte, now time.Time) error {
	result, err := a.pool.ExecContext(ctx,
		`UPDATE sessions SET revoked_at=$2
                WHERE token_hash=$1 AND revoked_at IS NULL
                  AND expires_at>$2 AND idle_expires_at>$2`, hash, now)
	if err != nil {
		return db.WrapError("revoke session", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return db.WrapError("count revoked sessions", err)
	}
	if n != 1 {
		return service.ErrUnauthorized
	}
	return nil
}

func (a *Auth) CleanupSessions(ctx context.Context, now time.Time) error {
	_, err := a.pool.ExecContext(ctx,
		`DELETE FROM sessions
			WHERE expires_at<=$1 OR idle_expires_at<=$1 OR revoked_at IS NOT NULL`, now)
	return db.WrapError("clean expired sessions", err)
}
