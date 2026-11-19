"""AI-owned PostgreSQL state. Core metadata is a read-only Go-owned view."""
import json
from contextlib import contextmanager
from uuid import uuid4

import psycopg
from psycopg.rows import dict_row
from psycopg.types.json import Jsonb

from faq_ai.jobs import JobError
from faq_ai.models import AppError


def lock_key(account, agent, document):
    return f"{account}:{agent}:{document}"


class PgStore:
    def __init__(self, dsn):
        self.dsn = dsn

    @contextmanager
    def connect(self):
        with psycopg.connect(self.dsn, row_factory=dict_row, connect_timeout=5,
                             options="-c statement_timeout=15000 -c lock_timeout=5000") as conn:
            yield conn

    @staticmethod
    def lock(conn, job):
        conn.execute("SELECT pg_advisory_xact_lock(hashtextextended(%s,0))",
                     (lock_key(job.account_id, job.agent_id, job.document_id),))

    def metadata(self, job, conn=None):
        if conn is None:
            with self.connect() as own:
                return self.metadata(job, own)
        row = conn.execute("""SELECT * FROM knowledge.ai_document_metadata
            WHERE account_id=%s AND agent_id=%s AND id=%s""",
            (job.account_id, job.agent_id, job.document_id)).fetchone()
        if row is None:
            raise JobError("document_not_found")
        expected = job.model_dump(mode="json")
        if (str(row["current_job_id"]) != str(job.job_id) or row["current_version"] != job.document_version
                or any(row["job_payload"].get(key) != expected[key] for key in expected
                       if key not in {"request_id", "trace_id"})):
            raise JobError("stale_job")
        deleting = row["deleted_at"] is not None or row["agent_deleted_at"] is not None
        if job.event_type == "document.delete":
            if row["status"] not in {"deleting", "deleted"} and not deleting:
                raise JobError("invalid_document_state")
        elif deleting or row["status"] in {"deleting", "deleted"}:
            raise JobError("document_deleted")
        elif row["status"] not in {"pending", "processing"}:
            raise JobError("invalid_document_state")
        return row

    def claim(self, job, owner, lease_seconds):
        with self.connect() as conn:
            conn.execute("INSERT INTO ai.receipts(job_id,identity_hash) VALUES(%s,%s) ON CONFLICT DO NOTHING",
                         (job.job_id, job.identity()))
            row = conn.execute("SELECT *, lease_until>now() AS leased FROM ai.receipts WHERE job_id=%s FOR UPDATE",
                               (job.job_id,)).fetchone()
            if row["identity_hash"] != job.identity():
                raise JobError("job_identity_conflict")
            if row["result"] is not None:
                return None, row["result"]
            if row["leased"]:
                raise JobError("job_leased", transient=True)
            row = conn.execute("""UPDATE ai.receipts SET owner=%s,fence=fence+1,
                lease_until=now()+%s*interval '1 second' WHERE job_id=%s RETURNING fence""",
                (owner, lease_seconds, job.job_id)).fetchone()
            return row["fence"], None

    def renew(self, job, owner, fence, seconds):
        with self.connect() as conn:
            result = conn.execute("""UPDATE ai.receipts SET lease_until=now()+%s*interval '1 second'
                WHERE job_id=%s AND owner=%s AND fence=%s AND result IS NULL AND lease_until>now()""",
                (seconds, job.job_id, owner, fence))
            if result.rowcount != 1:
                raise JobError("lease_lost", transient=True)

    @staticmethod
    def receipt_lock(conn, job, owner, fence):
        row = conn.execute("""SELECT * FROM ai.receipts WHERE job_id=%s FOR UPDATE""", (job.job_id,)).fetchone()
        live = conn.execute("SELECT lease_until>now() AS live FROM ai.receipts WHERE job_id=%s", (job.job_id,)).fetchone()
        if (not row or row["owner"] != owner or row["fence"] != fence or not live["live"]
                or row["result"] is not None):
            raise JobError("lease_lost", transient=True)

    def finish(self, job, owner, fence, result, chunks=None, vectors=None, fingerprint=None, dimension=None):
        with self.connect() as conn:
            self.lock(conn, job)
            self.receipt_lock(conn, job, owner, fence)
            if result["status"] == "completed":
                self.metadata(job, conn)  # Rechecked in the SAME transaction as the effect.
                if job.event_type == "document.delete":
                    conn.execute("DELETE FROM ai.generations WHERE account_id=%s AND agent_id=%s AND document_id=%s",
                                 (job.account_id, job.agent_id, job.document_id))
                else:
                    generation = job.job_id
                    conn.execute("""INSERT INTO ai.generations
                        (id,account_id,agent_id,document_id,document_version,model_fingerprint,dimension,chunk_count)
                        VALUES(%s,%s,%s,%s,%s,%s,%s,%s)""",
                        (generation, job.account_id, job.agent_id, job.document_id, job.document_version,
                         fingerprint, dimension, len(chunks)))
                    for chunk, vector in zip(chunks, vectors, strict=True):
                        if len(vector) != dimension:
                            raise JobError("embedding_dimension_mismatch")
                        conn.execute("""INSERT INTO ai.chunks VALUES(%s,%s,%s,%s,%s,%s,%s,%s::public.vector)""",
                                     (generation, chunk["number"], chunk["chunk_id"], chunk["content"],
                                      chunk["page"], chunk["heading"], chunk["start_char"], json.dumps(vector)))
                    conn.execute("UPDATE ai.generations SET complete=true WHERE id=%s", (generation,))
            conn.execute("""UPDATE ai.receipts SET result=%s,lease_until=NULL
                WHERE job_id=%s AND owner=%s AND fence=%s""", (Jsonb(result), job.job_id, owner, fence))
        return result

    def release(self, job, owner, fence):
        with self.connect() as conn:
            conn.execute("UPDATE ai.receipts SET lease_until=NULL WHERE job_id=%s AND owner=%s AND fence=%s AND result IS NULL",
                         (job.job_id, owner, fence))

    def published(self, job):
        with self.connect() as conn:
            conn.execute("UPDATE ai.receipts SET published_at=now() WHERE job_id=%s AND result IS NOT NULL", (job.job_id,))

    def cleanup(self):
        # Only Go's confirmed activation authorizes removal of older generations.
        with self.connect() as conn:
            rows = conn.execute("""SELECT * FROM knowledge.ai_document_metadata
                WHERE status='ready' AND active_generation IS NOT NULL AND agent_deleted_at IS NULL
                  AND deleted_at IS NULL AND active_version=current_version""").fetchall()
            for row in rows:
                conn.execute("SELECT pg_advisory_xact_lock(hashtextextended(%s,0))",
                             (lock_key(row["account_id"], row["agent_id"], row["id"]),))
                conn.execute("""DELETE FROM ai.generations g USING knowledge.ai_document_metadata d
                    WHERE g.account_id=d.account_id AND g.agent_id=d.agent_id AND g.document_id=d.id
                    AND d.account_id=%s AND d.agent_id=%s AND d.id=%s
                    AND d.status='ready' AND d.deleted_at IS NULL AND d.agent_deleted_at IS NULL
                    AND d.active_version=d.current_version AND g.id<>d.active_generation
                    AND g.document_version<d.active_version
                    AND EXISTS(SELECT 1 FROM ai.generations active WHERE active.id=d.active_generation
                        AND active.complete AND active.document_version=d.active_version)""",
                             (row["account_id"], row["agent_id"], row["id"]))

    def revision(self, scope):
        with self.connect() as conn:
            row = conn.execute("""SELECT COALESCE(max(knowledge_revision),0) AS revision
                FROM knowledge.ai_document_metadata WHERE account_id=%s AND agent_id=%s""",
                               (scope.account_id, scope.agent_id)).fetchone()
            return row["revision"]

    def snapshot(self, scope):
        # Reuse the tested RAG citation pipeline. Search filters metadata in SQL
        # before any chunk leaves the database; the bounded CLI corpus uses exact cosine.
        with self.connect() as conn:
            conn.execute("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY")
            revision = conn.execute("""SELECT COALESCE(max(knowledge_revision),0) AS revision
                FROM knowledge.ai_document_metadata WHERE account_id=%s AND agent_id=%s""",
                (scope.account_id, scope.agent_id)).fetchone()["revision"]
            rows = conn.execute("""SELECT c.chunk_id,c.content,c.start_char,c.page,c.heading,
                       c.embedding::text AS vector,d.id::text AS document_id,d.name AS filename,
                       g.model_fingerprint AS embedding_key,g.dimension,g.document_version
                FROM knowledge.ai_document_metadata d
                JOIN ai.generations g ON g.id=d.active_generation AND g.account_id=d.account_id
                  AND g.agent_id=d.agent_id AND g.document_id=d.id AND g.document_version=d.active_version AND g.complete
                JOIN ai.chunks c ON c.generation_id=g.id
                WHERE d.account_id=%s AND d.agent_id=%s AND d.deleted_at IS NULL AND d.agent_deleted_at IS NULL
                  AND d.status IN ('ready','processing') ORDER BY d.id,c.chunk_number LIMIT 10001""",
                (scope.account_id, scope.agent_id)).fetchall()
            if len(rows) > 10000:
                raise AppError("corpus_limit", "Корпус превышает лимит точного поиска этого сервиса.")
            return revision, [dict(row) for row in rows]
