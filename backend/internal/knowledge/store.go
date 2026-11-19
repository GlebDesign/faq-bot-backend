// Package knowledge owns document lifecycle and activation. Python only stages AI data.
package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/messaging"
)

//go:embed schema.sql
var schema string

var ErrConflict = errors.New("knowledge operation conflict")
var ErrStale = errors.New("stale knowledge result")
var idPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Store struct{ DB *sql.DB }

func (s Store) Migrate(ctx context.Context) error {
	return db.WithTx(ctx, s.DB, func(ctx context.Context, tx *sql.Tx) error {
		var current string
		if err := tx.QueryRowContext(ctx, "SELECT current_schema()").Scan(&current); err != nil {
			return err
		}
		if current != "public" {
			return errors.New("knowledge requires business tables in public schema")
		}
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('faq.knowledge.migrate.v1',0))"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, schema)
		return err
	})
}

type Submission struct {
	JobID       string `json:"job_id"`
	OperationID string `json:"operation_id"`
	AccountID   string `json:"account_id"`
	AgentID     string `json:"agent_id"`
	DocumentID  string `json:"document_id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Format      string `json:"format"`
	StorageKey  string `json:"storage_key"`
	SHA256      string `json:"sha256"`
	RequestID   string `json:"-"`
	TraceID     string `json:"-"`
}

func lock(ctx context.Context, tx *sql.Tx, account, agent, document string) error {
	_, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", account+":"+agent+":"+document)
	return err
}

func (s Store) Submit(ctx context.Context, in Submission) (out messaging.Message, err error) {
	for _, id := range []string{in.JobID, in.OperationID, in.AccountID, in.AgentID, in.DocumentID} {
		if !idPattern.MatchString(id) {
			return out, errors.New("invalid knowledge ID")
		}
	}
	if in.Kind != "document.index" && in.Kind != "document.reindex" && in.Kind != "document.delete" {
		return out, errors.New("invalid job kind")
	}
	if len(in.RequestID) < 1 || len(in.RequestID) > 128 || len(in.TraceID) < 1 || len(in.TraceID) > 128 {
		return out, errors.New("request and trace required")
	}
	if in.Kind != "document.delete" {
		if strings.TrimSpace(in.Name) == "" || len(in.Name) > 512 || !hashPattern.MatchString(in.SHA256) || len(in.StorageKey) > 512 || in.StorageKey == "" || strings.HasPrefix(in.StorageKey, "/") || strings.ContainsAny(in.StorageKey, "\\:") {
			return out, errors.New("invalid source metadata")
		}
		for _, part := range strings.Split(in.StorageKey, "/") {
			if part == ".." || part == "" {
				return out, errors.New("invalid storage key")
			}
		}
		switch in.Format {
		case "txt", "md", "text", "pdf", "docx":
		default:
			return out, errors.New("unsupported format")
		}
	}
	// Candidate job IDs and tracing do not change operation semantics.
	significant := in
	significant.JobID = ""
	raw, _ := json.Marshal(significant)
	hash := sha256.Sum256(raw)
	err = db.WithTx(ctx, s.DB, func(ctx context.Context, tx *sql.Tx) error {
		if err := lock(ctx, tx, in.AccountID, in.AgentID, in.DocumentID); err != nil {
			return err
		}
		var savedHash, payload []byte
		err := tx.QueryRowContext(ctx, `SELECT submission_hash,payload FROM knowledge.jobs WHERE account_id=$1 AND agent_id=$2 AND operation_id=$3`, in.AccountID, in.AgentID, in.OperationID).Scan(&savedHash, &payload)
		if err == nil {
			if !bytes.Equal(savedHash, hash[:]) {
				return ErrConflict
			}
			return json.Unmarshal(payload, &out)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var revision int64
		err = tx.QueryRowContext(ctx, `UPDATE agents SET knowledge_revision=knowledge_revision+1 WHERE account_id=$1 AND id=$2 AND deleted_at IS NULL RETURNING knowledge_revision`, in.AccountID, in.AgentID).Scan(&revision)
		if err != nil {
			return err
		}
		var version int64
		var status string
		err = tx.QueryRowContext(ctx, `SELECT current_version,status FROM knowledge.documents WHERE account_id=$1 AND agent_id=$2 AND id=$3 FOR UPDATE`, in.AccountID, in.AgentID, in.DocumentID).Scan(&version, &status)
		if in.Kind == "document.index" {
			if !errors.Is(err, sql.ErrNoRows) {
				if err != nil {
					return err
				}
				return ErrConflict
			}
			version = 1
			_, err = tx.ExecContext(ctx, `INSERT INTO knowledge.documents(id,account_id,agent_id,name,format,storage_key,source_sha256,current_version,status,current_job_id,knowledge_revision) VALUES($1,$2,$3,$4,$5,$6,$7,1,'pending',$8,$9)`, in.DocumentID, in.AccountID, in.AgentID, in.Name, in.Format, in.StorageKey, in.SHA256, in.JobID, revision)
		} else {
			if err != nil {
				return err
			}
			if status == "deleting" || status == "deleted" {
				return ErrConflict
			}
			version++
			if in.Kind == "document.delete" {
				_, err = tx.ExecContext(ctx, `UPDATE knowledge.documents SET current_version=$4,status='deleting',deleted_at=now(),current_job_id=$5,knowledge_revision=$6,error_code=NULL WHERE account_id=$1 AND agent_id=$2 AND id=$3`, in.AccountID, in.AgentID, in.DocumentID, version, in.JobID, revision)
			} else {
				_, err = tx.ExecContext(ctx, `UPDATE knowledge.documents SET current_version=$4,status='processing',current_job_id=$5,knowledge_revision=$6,name=$7,format=$8,storage_key=$9,source_sha256=$10,error_code=NULL WHERE account_id=$1 AND agent_id=$2 AND id=$3`, in.AccountID, in.AgentID, in.DocumentID, version, in.JobID, revision, in.Name, in.Format, in.StorageKey, in.SHA256)
			}
		}
		if err != nil {
			return err
		}
		out = messaging.Message{SchemaVersion: 1, EventType: in.Kind, JobID: in.JobID, OperationID: in.OperationID, AccountID: in.AccountID, AgentID: in.AgentID, DocumentID: in.DocumentID, DocumentVersion: version, KnowledgeRevision: revision, RequestID: in.RequestID, TraceID: in.TraceID, Attempt: 1}
		payload, err = json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO knowledge.jobs(id,operation_id,account_id,agent_id,document_id,submission_hash,payload) VALUES($1,$2,$3,$4,$5,$6,$7)`, in.JobID, in.OperationID, in.AccountID, in.AgentID, in.DocumentID, hash[:], payload)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO knowledge.outbox(job_id,payload) VALUES($1,$2)`, in.JobID, payload)
		return err
	})
	return
}

func (s Store) ApplyResult(ctx context.Context, result messaging.Message) error {
	stale := false
	err := db.WithTx(ctx, s.DB, func(ctx context.Context, tx *sql.Tx) error {
		if err := lock(ctx, tx, result.AccountID, result.AgentID, result.DocumentID); err != nil {
			return err
		}
		var payload, stored []byte
		var state string
		err := tx.QueryRowContext(ctx, `SELECT payload,status,result FROM knowledge.jobs WHERE id=$1 AND account_id=$2 AND agent_id=$3 FOR UPDATE`, result.JobID, result.AccountID, result.AgentID).Scan(&payload, &state, &stored)
		if err != nil {
			return err
		}
		var job messaging.Message
		if err = json.Unmarshal(payload, &job); err != nil {
			return err
		}
		if result.SchemaVersion != 1 || result.OperationID != job.OperationID || result.DocumentID != job.DocumentID || result.DocumentVersion != job.DocumentVersion || result.KnowledgeRevision != job.KnowledgeRevision || result.Attempt != job.Attempt || result.EventType != job.EventType+"."+result.Status || (result.Status != "completed" && result.Status != "failed") {
			return ErrConflict
		}
		encoded, _ := json.Marshal(result)
		if state == "completed" || state == "failed" {
			var previous messaging.Message
			if json.Unmarshal(stored, &previous) != nil {
				return ErrConflict
			}
			previous.RequestID = result.RequestID
			previous.TraceID = result.TraceID
			normalized, _ := json.Marshal(previous)
			if !bytes.Equal(normalized, encoded) {
				return ErrConflict
			}
			return nil
		}
		var currentJob, status string
		var active sql.NullString
		var deleted bool
		err = tx.QueryRowContext(ctx, `SELECT d.current_job_id::text,d.status,d.active_generation::text,a.deleted_at IS NOT NULL FROM knowledge.documents d JOIN agents a ON a.account_id=d.account_id AND a.id=d.agent_id WHERE d.account_id=$1 AND d.agent_id=$2 AND d.id=$3 FOR UPDATE OF d`, result.AccountID, result.AgentID, result.DocumentID).Scan(&currentJob, &status, &active, &deleted)
		if err != nil {
			return err
		}
		if currentJob != result.JobID || deleted || (job.EventType != "document.delete" && (status == "deleting" || status == "deleted")) {
			staleResult := result
			staleResult.Status = "failed"
			staleResult.EventType = job.EventType + ".failed"
			staleResult.ErrorCode = json.RawMessage(`"stale_result"`)
			staleJSON, _ := json.Marshal(staleResult)
			_, err = tx.ExecContext(ctx, `UPDATE knowledge.jobs SET status='failed',result=$2,error_code='stale_result' WHERE id=$1`, result.JobID, staleJSON)
			stale = true
			return err
		}
		if result.Status == "completed" {
			if job.EventType == "document.delete" {
				if status != "deleting" {
					return ErrStale
				}
				_, err = tx.ExecContext(ctx, `UPDATE knowledge.documents SET status='deleted',active_generation=NULL,active_version=NULL,error_code=NULL WHERE id=$1`, result.DocumentID)
			} else {
				if result.ChunksIndexed == nil || *result.ChunksIndexed < 1 {
					return ErrConflict
				}
				var count, actual int64
				err = tx.QueryRowContext(ctx, `SELECT g.chunk_count,count(c.generation_id) FROM ai.generations g JOIN ai.chunks c ON c.generation_id=g.id WHERE g.id=$1 AND g.account_id=$2 AND g.agent_id=$3 AND g.document_id=$4 AND g.document_version=$5 AND g.complete GROUP BY g.chunk_count`, result.JobID, result.AccountID, result.AgentID, result.DocumentID, result.DocumentVersion).Scan(&count, &actual)
				if err != nil {
					return err
				}
				if count != *result.ChunksIndexed || count != actual {
					return ErrConflict
				}
				_, err = tx.ExecContext(ctx, `UPDATE knowledge.documents SET status='ready',active_generation=$2,active_version=$3,error_code=NULL WHERE id=$1`, result.DocumentID, result.JobID, result.DocumentVersion)
			}
		} else {
			var code string
			if json.Unmarshal(result.ErrorCode, &code) != nil || code == "" || len(code) > 128 {
				return ErrConflict
			}
			target := "failed"
			if active.Valid && job.EventType == "document.reindex" {
				target = "ready"
			}
			if job.EventType == "document.delete" {
				target = "deleting"
			}
			_, err = tx.ExecContext(ctx, `UPDATE knowledge.documents SET status=$2,error_code=$3 WHERE id=$1`, result.DocumentID, target, code)
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE knowledge.jobs SET status=$2,result=$3,error_code=$4 WHERE id=$1`, result.JobID, result.Status, encoded, nullableError(result.ErrorCode))
		return err
	})
	if err == nil && stale {
		return ErrStale
	}
	return err
}

func nullableError(raw json.RawMessage) any {
	var code string
	if json.Unmarshal(raw, &code) == nil && code != "" {
		return code
	}
	return nil
}
