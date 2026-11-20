import threading

from langchain_core.embeddings import Embeddings

from faq_ai.models import AppError
from faq_ai.store import unit_vector


class BoundedEmbeddings(Embeddings):
    def __init__(self, inner, dimension=None, batch=16, concurrency=1, timeout=60):
        self.inner, self.dimension, self.batch = inner, dimension, batch
        self.gate, self.timeout = threading.BoundedSemaphore(concurrency), timeout

    def _invoke(self, texts, query=False):
        if not self.gate.acquire(timeout=self.timeout):
            raise AppError("embedding_busy", "Превышено время ожидания embedding-модели.")
        try:
            raw = [self.inner.embed_query(texts[0])] if query else self.inner.embed_documents(texts)
            if len(raw) != len(texts):
                raise AppError("invalid_embedding_count", "Число векторов не совпало с числом текстов.")
            vectors = []
            for vector in raw:
                if self.dimension is not None and len(vector) != self.dimension:
                    raise AppError("embedding_dimension_mismatch", "Размерность вектора не соответствует настройке.")
                try:
                    vectors.append(unit_vector(vector))
                except Exception as exc:
                    raise AppError("invalid_embedding", "Модель вернула некорректный вектор.") from exc
            return vectors
        except AppError:
            raise
        except Exception as exc:
            raise AppError("embedding_unavailable", "Embedding-модель недоступна.") from exc
        finally:
            self.gate.release()

    def embed_documents(self, texts):
        vectors = []
        for start in range(0, len(texts), self.batch):
            vectors.extend(self._invoke(texts[start:start + self.batch]))
        return vectors

    def embed_query(self, text):
        return self._invoke([text], query=True)[0]
