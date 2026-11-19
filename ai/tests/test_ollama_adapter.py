import json

import httpx
import pytest

from faq_ai.backends import ollama_backend
from faq_ai.config import Settings
from faq_ai.models import AppError, Scope
from faq_ai.rag import RagService
from faq_ai.store import Store


@pytest.mark.parametrize("quote_only", [False, True])
@pytest.mark.parametrize("response_kind", ["valid", "missing", "invalid_json", "unavailable"])
def test_real_langchain_adapters_with_mocked_ollama_http(tmp_path, monkeypatch, response_kind, quote_only):
    """Real ChatOllama/OllamaEmbeddings; HTTP fixtures, not model-quality evidence."""
    calls = []
    def send(client, request, **kwargs):
        assert request.url.host == "127.0.0.1"
        body = json.loads(request.read())
        calls.append(request.url.path)
        if request.url.path == "/api/embed":
            assert body["keep_alive"] == 0
            assert body["options"]["num_thread"] == 4
            inputs = body["input"] if isinstance(body["input"], list) else [body["input"]]
            return httpx.Response(200, request=request, json={"embeddings": [[1.0, 0.0] for _ in inputs]})
        assert request.url.path == "/api/chat"
        assert body["options"]["num_ctx"] == 4096
        assert body["options"]["num_predict"] == 768
        assert body["options"]["num_thread"] == 4
        assert body["think"] is False
        assert body["keep_alive"] == 0
        assert body["options"]["temperature"] == 0.7
        assert body["options"]["top_p"] == 0.8
        assert body["options"]["top_k"] == 20
        assert body["options"]["repeat_penalty"] == 1.1
        assert body["options"]["seed"] == 42
        assert "tools" not in body or not body["tools"]
        assert isinstance(body["format"], dict)
        if response_kind == "unavailable":
            raise httpx.ConnectError("offline", request=request)
        payload = json.loads(body["messages"][-1]["content"])
        if quote_only:
            excerpt = payload["excerpts"][0]
            assert excerpt["chunk_id"] == "S1"
            assert excerpt["quote"] == "Доставка в Москву стоит 500 рублей."
            assert body["format"]["properties"]["citation_ids"]["items"]["enum"] == ["Q1"]
            draft = {"citation_ids": [] if response_kind == "missing" else [excerpt["quote_id"]]}
        else:
            source = payload["sources"][0]
            assert body["format"]["$defs"]["Claim"]["properties"]["chunk_id"]["enum"] == ["S1"]
            assert source["text"] in body["format"]["$defs"]["Claim"]["properties"]["quote"]["enum"]
            draft = {"has_answer": response_kind != "missing", "claims": []}
            if draft["has_answer"]:
                draft["claims"] = [{"text": "Доставка стоит 500 рублей.", "chunk_id": source["chunk_id"],
                                    "quote": "Доставка в Москву стоит 500 рублей."}]
        content = "not json" if response_kind == "invalid_json" else json.dumps(draft, ensure_ascii=False)
        event = {"model": "test-generation", "message": {"role": "assistant", "content": content},
                 "done": True, "done_reason": "stop", "prompt_eval_count": 100, "eval_count": 20}
        return httpx.Response(200, request=request, text=json.dumps(event) + "\n",
                              headers={"content-type": "application/x-ndjson"})
    monkeypatch.setattr(httpx.Client, "send", send)
    for key, value in {
        "OLLAMA_BASE_URL": "http://127.0.0.1:11434",
        "LLM_MODEL": "test-generation", "EMBEDDING_MODEL": "test-embedding",
        "LLM_CONTEXT_TOKENS": "4096", "LLM_MAX_TOKENS": "768",
        "MODEL_THREADS": "4", "MODEL_KEEP_ALIVE": "0", "LLM_THINKING": "false",
        "LLM_TEMPERATURE": "0.7", "LLM_TOP_P": "0.8", "LLM_TOP_K": "20",
        "LLM_REPEAT_PENALTY": "1.1", "LLM_SEED": "42",
    }.items():
        monkeypatch.setenv(key, value)
    settings = Settings.from_env().model_copy(update={"quote_only_answers": quote_only})
    store = Store(tmp_path / "index.sqlite3")
    try:
        service = RagService(store, ollama_backend(settings), settings)
        scope = Scope(account_id="north", agent_id="sales")
        file = tmp_path / "delivery.txt"
        file.write_text("Доставка в Москву стоит 500 рублей.", encoding="utf-8")
        service.ingest(scope, file, "delivery")
        if response_kind in {"invalid_json", "unavailable"}:
            with pytest.raises(AppError) as error:
                service.ask(scope, "Доставка в Москву?")
            assert error.value.code == ("invalid_model_output" if response_kind == "invalid_json" else "runtime_unavailable")
        else:
            answer = service.ask(scope, "Доставка в Москву?")
            assert answer.has_answer == (response_kind == "valid")
            assert answer.usage_available and answer.input_tokens == 100 and answer.output_tokens == 20
            if answer.has_answer:
                assert answer.sources[0].chunk_id == store.snapshot(scope)[1][0]["chunk_id"]
        assert calls == ["/api/embed", "/api/embed", "/api/chat"]
    finally:
        store.close()


@pytest.mark.parametrize("selected", [["unknown"], ["Q1", "Q1"]])
def test_extractive_selection_rejects_unknown_or_duplicate_citations(selected):
    from types import SimpleNamespace
    from faq_ai.backends import select_excerpts
    class Structured:
        def invoke(self, messages):
            return {"parsed": {"citation_ids": selected}, "raw": SimpleNamespace(usage_metadata={})}
    class Chat:
        def with_structured_output(self, *args, **kwargs):
            return Structured()
    with pytest.raises(AppError) as error:
        select_excerpts(Chat(), {"question": "Доставка?", "sources": [
            {"chunk_id": "S1", "text": "Доставка в Москву стоит 500 рублей."}]})
    assert error.value.code == "invalid_model_output"
