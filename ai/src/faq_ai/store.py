import json
import math
import sqlite3
from pathlib import Path

from faq_ai.models import AppError, Scope


def unit_vector(values: list[float]) -> list[float]:
    if not values or not all(isinstance(x, (int, float)) and math.isfinite(x) for x in values):
        raise AppError("invalid_embedding", "Модель вернула некорректный вектор.")
    norm = math.sqrt(sum(x * x for x in values))
    if not math.isfinite(norm) or norm == 0:
        raise AppError("invalid_embedding", "Embedding имеет нулевую или некорректную длину.")
    return [x / norm for x in values]


class Store:
    """Local prototype store: SQL scope first, then linear cosine search in Python."""

    def __init__(self, path: Path):
        path.parent.mkdir(parents=True, exist_ok=True)
        self.db = sqlite3.connect(path, timeout=30)
        self.db.row_factory = sqlite3.Row
        self.db.execute("PRAGMA foreign_keys=ON")
        self.db.executescript("""
            CREATE TABLE IF NOT EXISTS scopes (
                account TEXT NOT NULL, agent TEXT NOT NULL, revision INTEGER NOT NULL,
                PRIMARY KEY(account, agent)
            );
            CREATE TABLE IF NOT EXISTS documents (
                account TEXT NOT NULL, agent TEXT NOT NULL, document_id TEXT NOT NULL,
                filename TEXT NOT NULL, fingerprint TEXT NOT NULL, embedding_key TEXT NOT NULL,
                dimension INTEGER NOT NULL,
                PRIMARY KEY(account, agent, document_id)
            );
            CREATE TABLE IF NOT EXISTS chunks (
                account TEXT NOT NULL, agent TEXT NOT NULL, document_id TEXT NOT NULL,
                chunk_id TEXT NOT NULL, content TEXT NOT NULL, start_char INTEGER NOT NULL,
                vector TEXT NOT NULL,
                PRIMARY KEY(account, agent, chunk_id),
                FOREIGN KEY(account, agent, document_id)
                    REFERENCES documents(account, agent, document_id) ON DELETE CASCADE
            );
        """)

    def close(self):
        self.db.close()

    @staticmethod
    def args(scope: Scope):
        return scope.account_id, scope.agent_id

    def revision(self, scope: Scope) -> int:
        row = self.db.execute("SELECT revision FROM scopes WHERE account=? AND agent=?", self.args(scope)).fetchone()
        return row[0] if row else 0

    def bump(self, scope: Scope):
        self.db.execute("""INSERT INTO scopes VALUES (?, ?, 1)
            ON CONFLICT(account, agent) DO UPDATE SET revision=revision+1""", self.args(scope))

    def replace(self, scope: Scope, document_id: str, filename: str, fingerprint: str,
                embedding_key: str, texts: list[dict], vectors: list[list[float]]) -> int:
        # Embeddings and all validation finish before the transaction touches the active version.
        args = self.args(scope)
        with self.db:
            old = self.db.execute("SELECT fingerprint FROM documents WHERE account=? AND agent=? AND document_id=?",
                                  (*args, document_id)).fetchone()
            if old and old[0] == fingerprint:
                return self.revision(scope)
            self.db.execute("DELETE FROM documents WHERE account=? AND agent=? AND document_id=?", (*args, document_id))
            self.db.execute("INSERT INTO documents VALUES (?, ?, ?, ?, ?, ?, ?)",
                            (*args, document_id, filename, fingerprint, embedding_key, len(vectors[0])))
            self.db.executemany("INSERT INTO chunks VALUES (?, ?, ?, ?, ?, ?, ?)", [
                (*args, document_id, t["chunk_id"], t["content"], t["start_char"], json.dumps(v))
                for t, v in zip(texts, vectors, strict=True)
            ])
            self.bump(scope)
        return self.revision(scope)

    def snapshot(self, scope: Scope) -> tuple[int, list[dict]]:
        self.db.execute("BEGIN")
        try:
            revision = self.revision(scope)
            rows = self.db.execute("""SELECT c.*, d.filename, d.embedding_key, d.dimension
                FROM chunks c JOIN documents d USING(account, agent, document_id)
                WHERE c.account=? AND c.agent=? ORDER BY c.document_id, c.start_char""", self.args(scope)).fetchall()
            return revision, [dict(row) for row in rows]
        finally:
            self.db.rollback()

    def delete(self, scope: Scope, document_id: str) -> bool:
        with self.db:
            result = self.db.execute("DELETE FROM documents WHERE account=? AND agent=? AND document_id=?",
                                     (*self.args(scope), document_id))
            if result.rowcount:
                self.bump(scope)
            return bool(result.rowcount)

    def status(self, scope: Scope) -> dict:
        rows = self.db.execute("""SELECT d.document_id, d.filename, d.embedding_key, d.dimension,
            (SELECT COUNT(*) FROM chunks c WHERE c.account=d.account AND c.agent=d.agent
             AND c.document_id=d.document_id) AS chunks
            FROM documents d WHERE d.account=? AND d.agent=? ORDER BY d.document_id""", self.args(scope)).fetchall()
        return {"knowledge_version": self.revision(scope), "documents": [dict(row) for row in rows]}
