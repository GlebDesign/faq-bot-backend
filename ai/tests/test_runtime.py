import pytest
from langchain_core.embeddings import Embeddings

from faq_ai.jobs import JobError
from faq_ai.runtime import BoundedEmbeddings


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
    with pytest.raises(JobError) as error:
        BoundedEmbeddings(Fake(vector),2,2,1,0.01).embed_query('a')
    assert error.value.code == code


def test_embedding_concurrency_is_bounded_and_released_on_failure():
    adapter=BoundedEmbeddings(Fake([1.,0.]),2,2,1,0.01)
    adapter.gate.acquire()
    with pytest.raises(JobError) as error:
        adapter.embed_query('a')
    assert error.value.code=='embedding_busy' and error.value.transient
    adapter.gate.release()
    assert adapter.embed_query('a') == [1.,0.]


def test_runtime_refuses_changed_weights_without_demo_fallback(tmp_path,monkeypatch):
    import importlib
    import io
    from faq_ai.config import Settings
    from faq_ai.service_config import ServiceSettings
    module=importlib.import_module('faq_ai.runtime')
    monkeypatch.setattr(module,'urlopen',lambda *args,**kwargs:io.BytesIO(
        b'{"models":[{"name":"embed","digest":"changed"},{"name":"llm","digest":"pinned"}]}'))
    service=ServiceSettings(database_url='postgresql://unused',source_root=tmp_path,
        service_token='x'*40,embedding_digest='expected',generation_digest='pinned')
    with pytest.raises(JobError) as error:
        module.runtime(Settings(llm_model='llm',embedding_model='embed'),service)
    assert error.value.code=='model_fingerprint_mismatch'
