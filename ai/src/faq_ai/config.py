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
    llm_context_tokens: int = Field(default=8192, ge=2048, le=32768)
    llm_max_tokens: int = Field(default=1400, ge=128, le=4096)
    model_threads: int | None = Field(default=None, ge=1, le=64)
    model_keep_alive: int | None = Field(default=None, ge=0, le=3600)
    llm_thinking: bool | None = None
    llm_temperature: float = Field(default=0, ge=0, le=2)
    llm_top_p: float | None = Field(default=None, gt=0, le=1)
    llm_top_k: int | None = Field(default=None, ge=0, le=1000)
    llm_repeat_penalty: float | None = Field(default=None, gt=0, le=2)
    llm_seed: int | None = None
    embedding_dimension: int | None = Field(default=None, ge=1, le=8192)
    embedding_batch_size: int = Field(default=16, ge=1, le=128)
    model_concurrency: int = Field(default=1, ge=1, le=16)

    @model_validator(mode="after")
    def validate_limits(self):
        if self.chunk_overlap >= self.chunk_size:
            raise ValueError("CHUNK_OVERLAP must be smaller than CHUNK_SIZE")
        url = urlparse(self.ollama_base_url)
        if url.scheme not in {"http", "https"} or not url.hostname or url.username or url.password or url.query or url.fragment:
            raise ValueError("OLLAMA_BASE_URL must be an HTTP(S) URL without credentials, query or fragment")
        if url.port is not None and not 1 <= url.port <= 65535:
            raise ValueError("OLLAMA_BASE_URL port is out of range")
        return self

    @classmethod
    def from_env(cls):
        fields = {
            "ollama_base_url": "OLLAMA_BASE_URL", "llm_model": "LLM_MODEL",
            "embedding_model": "EMBEDDING_MODEL", "embedding_revision": "EMBEDDING_REVISION",
            "timeout": "OLLAMA_TIMEOUT", "chunk_size": "CHUNK_SIZE",
            "chunk_overlap": "CHUNK_OVERLAP", "top_k": "TOP_K",
            "min_score": "MIN_SCORE", "context_chars": "CONTEXT_CHARS",
            "llm_context_tokens": "LLM_CONTEXT_TOKENS", "llm_max_tokens": "LLM_MAX_TOKENS",
            "model_threads": "MODEL_THREADS", "model_keep_alive": "MODEL_KEEP_ALIVE",
            "llm_thinking": "LLM_THINKING",
            "llm_temperature": "LLM_TEMPERATURE", "llm_top_p": "LLM_TOP_P",
            "llm_top_k": "LLM_TOP_K", "llm_repeat_penalty": "LLM_REPEAT_PENALTY",
            "llm_seed": "LLM_SEED",
            "embedding_dimension": "EMBEDDING_DIMENSION",
            "embedding_batch_size": "EMBEDDING_BATCH_SIZE",
            "model_concurrency": "MODEL_CONCURRENCY",
        }
        return cls(**{key: os.environ[env] for key, env in fields.items() if env in os.environ})

    def require_models(self):
        if not self.llm_model.strip() or not self.embedding_model.strip():
            raise AppError("configuration_error", "Задай LLM_MODEL и EMBEDDING_MODEL или запусти demo.")
