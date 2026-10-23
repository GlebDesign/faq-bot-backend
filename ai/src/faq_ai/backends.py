import hashlib
import json
import re
from dataclasses import dataclass
from typing import Any

from langchain_core.embeddings import Embeddings
from langchain_core.exceptions import OutputParserException
from langchain_core.runnables import RunnableLambda
from langchain_ollama import ChatOllama, OllamaEmbeddings

from faq_ai.config import Settings
from faq_ai.models import AnswerDraft, AppError, Claim


@dataclass
class Backend:
    name: str
    generation_model: str
    embedding_key: str
    embeddings: Embeddings
    generator: Any


def ollama_backend(settings: Settings) -> Backend:
    settings.require_models()
    chat = ChatOllama(
        model=settings.llm_model, base_url=settings.ollama_base_url,
        temperature=0, num_predict=1400, num_ctx=8192,
        client_kwargs={"timeout": settings.timeout},
    )
    structured = chat.with_structured_output(AnswerDraft, method="json_schema", include_raw=True)

    def generate(prompt):
        try:
            result = structured.invoke(prompt)
        except OutputParserException as exc:
            raise AppError("invalid_model_output", "Ответ модели не соответствует JSON-схеме.") from exc
        except Exception as exc:
            raise AppError("runtime_unavailable", "Ollama недоступен, модель не загружена или запрос отклонён.") from exc
        if result.get("parsing_error") or not isinstance(result.get("parsed"), AnswerDraft):
            raise AppError("invalid_model_output", "Модель вернула непроверяемую структуру ответа.")
        usage = result["raw"].usage_metadata or {}
        return result["parsed"], usage

    return Backend(
        "ollama", settings.llm_model,
        f"ollama:{settings.ollama_base_url}:{settings.embedding_model}:{settings.embedding_revision}",
        OllamaEmbeddings(model=settings.embedding_model, base_url=settings.ollama_base_url,
                         client_kwargs={"timeout": settings.timeout}),
        RunnableLambda(generate),
    )


def tokens(text: str) -> set[str]:
    stop = {"в", "во", "на", "и", "а", "по", "для", "из", "как", "где", "ли", "это", "у", "с", "до"}
    return set(re.findall(r"[а-яёa-z0-9]+", text.lower().replace("ё", "е"))) - stop


class DemoEmbeddings(Embeddings):
    """Deterministic token hashes, NOT a semantic embedding model."""

    def embed_query(self, text: str) -> list[float]:
        vector = [0.0] * 8192
        for token in tokens(text):
            index = int.from_bytes(hashlib.sha256(token.encode()).digest()[:4], "big") % len(vector)
            vector[index] += 1
        # Empty queries remain processable; they won't overlap ordinary documents.
        if not any(vector):
            vector[-1] = 1
        return vector

    def embed_documents(self, texts: list[str]) -> list[list[float]]:
        return [self.embed_query(text) for text in texts]


def demo_backend() -> Backend:
    def extract(prompt):
        payload = json.loads(prompt.to_messages()[-1].content)
        question_tokens = tokens(payload["question"])
        candidates = []
        for source in payload["sources"]:
            for line in source["text"].splitlines():
                line = line.strip()
                if len(line) >= 8 and not line.startswith("#"):
                    overlap = len(tokens(line) & question_tokens)
                    if overlap:
                        candidates.append((overlap, source["chunk_id"], line))
        if not candidates:
            return AnswerDraft(has_answer=False, claims=[]), {}
        _, chunk_id, line = max(candidates, key=lambda item: item[0])
        return AnswerDraft(has_answer=True, claims=[Claim(text=line, chunk_id=chunk_id, quote=line)]), {}

    return Backend("demo-extractive", "none:deterministic-extraction", "demo-token-hash-v1",
                   DemoEmbeddings(), RunnableLambda(extract))
