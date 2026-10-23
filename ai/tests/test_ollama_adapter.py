import json

import httpx
import pytest

from faq_ai.backends import ollama_backend
from faq_ai.config import Settings
from faq_ai.models import AppError, Scope
from faq_ai.rag import RagService
from faq_ai.store import Store


@pytest.mark.parametrize("response_kind", ["valid", "missing", "invalid_json", "unavailable"])
def test_real_langchain_adapters_with_mocked_ollama_http(tmp_path, monkeypatch, response_kind):
    """Real ChatOllama/OllamaEmbeddings; HTTP fixtures, not model-quality evidence."""
    calls = []
    def send(client, request, **kwargs):
        assert request.url.host == "127.0.0.1"
        body = json.loads(request.read())
        calls.append(request.url.path)
        if request.url.path == "/api/embed":
            inputs = body["input"] if isinstance(body["input"], list) else [body["input"]]
            return httpx.Response(200, request=request, json={"embeddings": [[1.0, 0.0] for _ in inputs]})
        assert request.url.path == "/api/chat"
        assert "tools" not in body or not body["tools"]
        assert isinstance(body["format"], dict)
        if response_kind == "unavailable":
            raise httpx.ConnectError("offline", request=request)
        payload = json.loads(body["messages"][-1]["content"])
        source = payload["sources"][0]
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
    settings = Settings(llm_model="test-generation", embedding_model="test-embedding")
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
        assert calls == ["/api/embed", "/api/embed", "/api/chat"]
    finally:
        store.close()
