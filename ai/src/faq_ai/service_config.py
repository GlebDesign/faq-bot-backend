"""Configuration for the separately launched RPC server and AMQP worker."""
import os
from pathlib import Path

from pydantic import Field, SecretStr, field_validator

from faq_ai.models import StrictModel


class ServiceSettings(StrictModel):
    database_url: SecretStr
    amqp_url: SecretStr = SecretStr("amqp://guest:guest@127.0.0.1:5672/%2F")
    service_token: SecretStr
    source_root: Path
    grpc_listen: str = "127.0.0.1:50051"
    backend: str = "ollama"
    embedding_dimension: int = Field(default=1024, ge=1, le=8192)
    embedding_digest: str = ""
    generation_digest: str = ""
    embedding_batch: int = Field(default=16, ge=1, le=128)
    concurrency: int = Field(default=1, ge=1, le=16)
    queue_size: int = Field(default=2, ge=0, le=32)
    rpc_timeout: float = Field(default=180, gt=0, le=300)
    lease_seconds: int = Field(default=300, ge=30, le=1800)
    max_attempts: int = Field(default=3, ge=1, le=10)
    retry_seconds: int = Field(default=5, ge=1, le=300)
    parse_timeout: int = Field(default=20, ge=1, le=60)
    max_file_bytes: int = Field(default=10_000_000, ge=1024, le=10_000_000)
    prefetch: int = Field(default=1, ge=1, le=8)

    @field_validator("service_token")
    @classmethod
    def token_length(cls, value):
        if len(value.get_secret_value()) < 32:
            raise ValueError("AI_SERVICE_TOKEN needs at least 32 characters")
        return value

    @field_validator("backend")
    @classmethod
    def known_backend(cls, value):
        if value not in {"ollama", "demo"}:
            raise ValueError("AI_BACKEND must be ollama or explicit demo")
        return value

    @classmethod
    def from_env(cls):
        keys = {
            "database_url": "AI_DATABASE_URL", "amqp_url": "AI_AMQP_URL",
            "service_token": "AI_SERVICE_TOKEN", "source_root": "AI_SOURCE_ROOT",
            "grpc_listen": "AI_GRPC_LISTEN", "backend": "AI_BACKEND",
            "embedding_dimension": "AI_EMBEDDING_DIMENSION", "embedding_digest": "AI_EMBEDDING_DIGEST",
            "generation_digest": "AI_GENERATION_DIGEST", "embedding_batch": "AI_EMBEDDING_BATCH",
            "concurrency": "AI_CONCURRENCY", "queue_size": "AI_QUEUE_SIZE",
            "rpc_timeout": "AI_RPC_TIMEOUT", "lease_seconds": "AI_LEASE_SECONDS",
            "max_attempts": "AI_MAX_ATTEMPTS", "retry_seconds": "AI_RETRY_SECONDS",
            "parse_timeout": "AI_PARSE_TIMEOUT", "max_file_bytes": "AI_MAX_FILE_BYTES",
            "prefetch": "AI_PREFETCH",
        }
        return cls(**{key: os.environ[env] for key, env in keys.items() if env in os.environ})
