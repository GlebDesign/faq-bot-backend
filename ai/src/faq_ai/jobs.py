"""Versioned queue contract, compatible with backend/internal/messaging.Message."""
import hashlib
import json
from typing import Literal
from uuid import UUID

from pydantic import Field

from faq_ai.models import StrictModel


class Job(StrictModel):
    schema_version: Literal[1]
    event_type: Literal["document.index", "document.delete", "document.reindex"]
    job_id: UUID
    operation_id: UUID
    account_id: UUID
    agent_id: UUID
    document_id: UUID
    document_version: int = Field(ge=1)
    knowledge_revision: int = Field(ge=1)
    request_id: str = Field(min_length=1, max_length=128)
    trace_id: str = Field(min_length=1, max_length=128)
    attempt: int = Field(ge=1, le=100)

    def identity(self):
        data = self.model_dump(mode="json", exclude={"request_id", "trace_id"})
        return hashlib.sha256(json.dumps(data, sort_keys=True).encode()).hexdigest()

    def result(self, error=None, count=0):
        data = self.model_dump(mode="json")
        data.update(event_type=self.event_type + (".failed" if error else ".completed"),
                    status="failed" if error else "completed", error_code=error)
        if not error and self.event_type != "document.delete":
            data["chunks_indexed"] = count
        return data


class JobError(Exception):
    def __init__(self, code, transient=False):
        self.code, self.transient = code, transient
        super().__init__(code)
