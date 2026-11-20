import pytest
from langchain_core.embeddings import Embeddings

from faq_ai.models import AppError
from faq_ai.embeddings import BoundedEmbeddings


class Fake(Embeddings):
    def __init__(self, vector):
        self.vector, self.sizes = vector, []
    def embed_documents(self, texts):
        self.sizes.append(len(texts))
        return [self.vector for _ in texts]
    def embed_query(self, text):
        return self.vector


def test_embeddings_batch_and_query_use_same_adapter():
    inner=Fake([3.,4.])
    adapter=BoundedEmbeddings(inner,2,2,1,0.01)
    assert adapter.embed_documents(['a','b','c']) == [[0.6,0.8]]*3
    assert inner.sizes == [2,1]
    assert adapter.embed_query('a') == [0.6,0.8]


@pytest.mark.parametrize('vector,code', [([1.], 'embedding_dimension_mismatch'),
    ([float('nan'),1.], 'invalid_embedding'), ([0.,0.], 'invalid_embedding')])
def test_embeddings_reject_invalid_vectors(vector,code):
    with pytest.raises(AppError) as error:
        BoundedEmbeddings(Fake(vector),2,2,1,0.01).embed_query('a')
    assert error.value.code == code


def test_embedding_concurrency_is_bounded_and_released_on_failure():
    adapter=BoundedEmbeddings(Fake([1.,0.]),2,2,1,0.01)
    adapter.gate.acquire()
    with pytest.raises(AppError) as error:
        adapter.embed_query('a')
    assert error.value.code=='embedding_busy'
    adapter.gate.release()
    assert adapter.embed_query('a') == [1.,0.]
