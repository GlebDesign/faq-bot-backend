from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, model_validator


class AppError(Exception):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


class StrictModel(BaseModel):
    model_config = ConfigDict(extra="forbid", str_strip_whitespace=True)


class Scope(StrictModel):
    account_id: str = Field(min_length=1, max_length=128)
    agent_id: str = Field(min_length=1, max_length=128)


class Claim(StrictModel):
    text: str = Field(min_length=1, max_length=1200)
    chunk_id: str = Field(min_length=1, max_length=128)
    quote: str = Field(min_length=8, max_length=800)


class AnswerDraft(StrictModel):
    has_answer: bool
    claims: list[Claim] = Field(max_length=5)

    @model_validator(mode="after")
    def consistent(self):
        if self.has_answer != bool(self.claims):
            raise ValueError("has_answer must match the presence of claims")
        return self


class Source(StrictModel):
    document_id: str
    chunk_id: str
    filename: str
    start_char: int
    quote: str
    score: float


class Answer(StrictModel):
    answer: str
    has_answer: bool
    reason: Literal["answered", "empty_knowledge", "insufficient_evidence", "invalid_citations"]
    sources: list[Source] = Field(default_factory=list)
    knowledge_version: int
    backend: str
    generation_model: str
    embedding_model: str
    prompt_version: str = "faq-rag-v1"
    usage_available: bool = False
    input_tokens: int | None = None
    output_tokens: int | None = None
    elapsed_ms: float = 0
