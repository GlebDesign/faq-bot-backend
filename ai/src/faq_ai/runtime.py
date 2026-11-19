import json
import os
import threading
from urllib.request import urlopen

from langchain_core.embeddings import Embeddings

from faq_ai.backends import demo_backend, ollama_backend
from faq_ai.jobs import JobError
from faq_ai.store import unit_vector


class BoundedEmbeddings(Embeddings):
    def __init__(self, inner, dimension, batch, concurrency, timeout, verify=lambda: None):
        self.inner, self.dimension, self.batch = inner, dimension, batch
        self.gate, self.timeout, self.verify = threading.BoundedSemaphore(concurrency), timeout, verify

    def _invoke(self, texts, query=False):
        if not self.gate.acquire(timeout=self.timeout):
            raise JobError("embedding_busy", transient=True)
        try:
            self.verify()
            raw = [self.inner.embed_query(texts[0])] if query else self.inner.embed_documents(texts)
            if len(raw) != len(texts):
                raise JobError("invalid_embedding_count")
            vectors = []
            for vector in raw:
                if len(vector) != self.dimension:
                    raise JobError("embedding_dimension_mismatch")
                try:
                    vectors.append(unit_vector(vector))
                except Exception as exc:
                    raise JobError("invalid_embedding") from exc
            return vectors
        except JobError:
            raise
        except Exception as exc:
            raise JobError("embedding_unavailable", transient=True) from exc
        finally:
            self.gate.release()

    def embed_documents(self, texts):
        vectors = []
        for start in range(0, len(texts), self.batch):
            vectors.extend(self._invoke(texts[start:start + self.batch]))
        return vectors

    def embed_query(self, text):
        return self._invoke([text], query=True)[0]


def runtime(settings, service):
    if any(os.getenv(key, "").lower() in {"true", "1"} for key in ("LANGCHAIN_TRACING_V2", "LANGSMITH_TRACING", "LANGCHAIN_TRACING")):
        raise ValueError("Disable external LangSmith tracing for local knowledge")
    def verify():
        try:
            with urlopen(settings.ollama_base_url.rstrip('/') + '/api/tags', timeout=5) as response:
                tags = {m['name']: m['digest'] for m in json.load(response)['models']}
            if (tags.get(settings.embedding_model) != service.embedding_digest
                    or tags.get(settings.llm_model) != service.generation_digest):
                raise JobError("model_fingerprint_mismatch")
        except JobError:
            raise
        except Exception as exc:
            raise JobError("runtime_unavailable", transient=True) from exc

    if service.backend == "demo":
        backend = demo_backend()
        if service.embedding_dimension != 8192:
            raise ValueError("Explicit demo backend requires dimension 8192")
        check = lambda: None
    else:
        if not service.embedding_digest or not service.generation_digest:
            raise ValueError("Pin AI_EMBEDDING_DIGEST and AI_GENERATION_DIGEST")
        verify()
        backend = ollama_backend(settings)
        backend.embedding_key = f"ollama:{settings.embedding_model}:{service.embedding_digest}:{service.embedding_dimension}"
        check = verify
    backend.embeddings = BoundedEmbeddings(backend.embeddings, service.embedding_dimension,
                                          service.embedding_batch, service.concurrency,
                                          settings.timeout, check)
    return backend
