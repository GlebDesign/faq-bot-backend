import os
from urllib.parse import urlparse

from pydantic import Field, model_validator

from faq_ai.models import AppError, StrictModel


class Settings(StrictModel):
    ollama_base_url: str = "http://127.0.0.1:11434"
    llm_model: str = ""
    embedding_model: str = ""
    embedding_revision: str = "1"
    timeout: float = Field(default=60, gt=0, le=300)
    chunk_size: int = Field(default=800, ge=100, le=4000)
    chunk_overlap: int = Field(default=120, ge=0)
    top_k: int = Field(default=4, ge=1, le=10)
    min_score: float = Field(default=0.5, ge=0, le=1)
    context_chars: int = Field(default=6000, ge=1000, le=16000)

    @model_validator(mode="after")
    def validate_limits(self):
        if self.chunk_overlap >= self.chunk_size:
            raise ValueError("CHUNK_OVERLAP must be smaller than CHUNK_SIZE")
        url = urlparse(self.ollama_base_url)
        if url.scheme not in {"http", "https"} or not url.hostname or url.username or url.password:
            raise ValueError("OLLAMA_BASE_URL must be an HTTP(S) URL without credentials")
        return self

    @classmethod
    def from_env(cls):
        fields = {
            "ollama_base_url": "OLLAMA_BASE_URL", "llm_model": "LLM_MODEL",
            "embedding_model": "EMBEDDING_MODEL", "embedding_revision": "EMBEDDING_REVISION",
            "timeout": "OLLAMA_TIMEOUT", "chunk_size": "CHUNK_SIZE",
            "chunk_overlap": "CHUNK_OVERLAP", "top_k": "TOP_K",
            "min_score": "MIN_SCORE", "context_chars": "CONTEXT_CHARS",
        }
        return cls(**{key: os.environ[env] for key, env in fields.items() if env in os.environ})

    def require_models(self):
        if not self.llm_model.strip() or not self.embedding_model.strip():
            raise AppError("configuration_error", "Задай LLM_MODEL и EMBEDDING_MODEL или запусти demo.")
