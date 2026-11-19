"""Run a small real-model acceptance check; no fake backend or HTTP mocks."""
import json
import sys
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path
from urllib.request import urlopen

from faq_ai.backends import ollama_backend
from faq_ai.config import Settings
from faq_ai.models import Scope
from faq_ai.rag import RagService
from faq_ai.store import Store


def main():
    if hasattr(sys.stdout, 'reconfigure'):
        sys.stdout.reconfigure(encoding='utf-8')
    settings = Settings.from_env()
    settings.require_models()
    runtime = Path(__file__).resolve().parents[1] / '.runtime'
    runtime.mkdir(parents=True, exist_ok=True)
    with urlopen(settings.ollama_base_url.rstrip('/') + '/api/tags', timeout=10) as response:
        model_metadata = json.load(response)['models']
    results = []
    started = time.perf_counter()
    with tempfile.TemporaryDirectory(prefix='real-smoke-', dir=runtime) as temporary:
        store = Store(Path(temporary) / 'index.sqlite3')
        try:
            service = RagService(store, ollama_backend(settings), settings)
            north = Scope(account_id='smoke-north', agent_id='sales')
            south = Scope(account_id='smoke-south', agent_id='sales')
            other_agent = Scope(account_id='smoke-north', agent_id='other')
            for scope, text in [
                (north, 'Доставка в Москву занимает 2 рабочих дня и стоит 500 рублей.'),
                (south, 'Доставка в Москву занимает 7 рабочих дней и стоит 900 рублей.'),
                (other_agent, 'Доставка в Москву занимает 4 рабочих дня и стоит 700 рублей.'),
            ]:
                service.ingest_text(scope, text, 'delivery', 'Учебные условия доставки')
            dimension = store.status(north)['documents'][0]['dimension']
            cases = [
                ('known', north, 'Сколько стоит доставка в Москву?', '500'),
                ('missing_fact', north, 'Сколько стоит доставка в Казань?', None),
                ('unrelated', north, 'Кто открыл Плутон?', None),
                ('other_account', south, 'Сколько стоит доставка в Москву?', '900'),
                ('other_agent', other_agent, 'Сколько стоит доставка в Москву?', '700'),
            ]
            for name, scope, question, expected in cases:
                answer = service.ask(scope, question)
                passed = (answer.has_answer and expected in answer.answer and bool(answer.sources)
                          if expected else not answer.has_answer)
                results.append({'case': name, 'question': question, 'passed': passed,
                                'response': answer.model_dump()})
                print(json.dumps({'case': name, 'passed': passed, 'answer': answer.answer,
                                  'elapsed_ms': answer.elapsed_ms}, ensure_ascii=False), flush=True)
            service.ingest_text(north, 'Доставка в Москву стоит 600 рублей.', 'delivery', 'Учебные условия доставки')
            changed = service.ask(north, 'Сколько стоит доставка в Москву?')
            results.append({'case': 'replacement', 'passed': changed.has_answer and '600' in changed.answer
                            and '500' not in changed.answer, 'response': changed.model_dump()})
            store.delete(north, 'delivery')
            deleted = service.ask(north, 'Сколько стоит доставка в Москву?')
            results.append({'case': 'deletion', 'passed': not deleted.has_answer
                            and deleted.reason == 'empty_knowledge', 'response': deleted.model_dump()})
            file_scope = Scope(account_id='file-example', agent_id='sales')
            service.ingest(file_scope, Path(__file__).resolve().parents[1] / 'examples' / 'delivery.md', 'delivery')
            for name, question, expected in [
                ('file_price', 'Сколько стоит доставка в Москву?', '500'),
                ('file_pickup', 'Когда работает самовывоз?', '10:00'),
                ('file_missing', 'Сколько стоит доставка в Казань?', None),
            ]:
                answer = service.ask(file_scope, question)
                passed = (answer.has_answer and expected in answer.answer and bool(answer.sources)
                          if expected else not answer.has_answer)
                results.append({'case': name, 'question': question, 'passed': passed,
                                'response': answer.model_dump()})
                print(json.dumps({'case': name, 'passed': passed, 'answer': answer.answer,
                                  'elapsed_ms': answer.elapsed_ms}, ensure_ascii=False), flush=True)
        finally:
            store.close()
    report = {
        'created_at': datetime.now(timezone.utc).isoformat(),
        'backend': 'real Ollama, no mocks', 'settings': settings.model_dump(),
        'models': model_metadata, 'embedding_dimension': dimension,
        'passed': all(case['passed'] for case in results),
        'total_seconds': round(time.perf_counter() - started, 2), 'cases': results,
        'limitations': 'Small synthetic smoke set. Not a held-out quality benchmark or load test.',
    }
    report_path = runtime / 'last-real-smoke.json'
    report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf-8')
    print(json.dumps({'passed': report['passed'], 'cases': len(results),
                      'embedding_dimension': dimension, 'report': str(report_path)}, ensure_ascii=False))
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
