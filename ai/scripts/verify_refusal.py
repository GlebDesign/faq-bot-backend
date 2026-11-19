"""Force retrieval of a real source to check the LLM's insufficient-evidence decision."""
import json
import sys
import tempfile
import time
from pathlib import Path

from faq_ai.backends import ollama_backend
from faq_ai.config import Settings
from faq_ai.models import Scope
from faq_ai.rag import RagService
from faq_ai.store import Store


def main():
    if hasattr(sys.stdout, 'reconfigure'):
        sys.stdout.reconfigure(encoding='utf-8')
    settings = Settings.from_env().model_copy(update={'min_score': 0.0})
    runtime = Path(__file__).resolve().parents[1] / '.runtime'
    runtime.mkdir(parents=True, exist_ok=True)
    started = time.perf_counter()
    with tempfile.TemporaryDirectory(prefix='real-refusal-', dir=runtime) as temporary:
        store = Store(Path(temporary) / 'index.sqlite3')
        try:
            service = RagService(store, ollama_backend(settings), settings)
            scope = Scope(account_id='refusal-test', agent_id='sales')
            service.ingest_text(scope, 'Доставка в Москву стоит 500 рублей.', 'delivery', 'Условия доставки')
            answer = service.ask(scope, 'Какой номер телефона у службы доставки в Москву?')
        finally:
            store.close()
    report = {
        'case': 'llm_refuses_missing_fact',
        'passed': not answer.has_answer and answer.usage_available,
        'forced_retrieval': True,
        'note': 'MIN_SCORE=0 only in this isolated test, to ensure the LLM sees a source.',
        'response': answer.model_dump(), 'total_seconds': round(time.perf_counter() - started, 2),
    }
    (runtime / 'last-real-refusal.json').write_text(
        json.dumps(report, ensure_ascii=False, indent=2), encoding='utf-8')
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
