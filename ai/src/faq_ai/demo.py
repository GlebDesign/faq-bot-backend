import tempfile
from pathlib import Path

from faq_ai.backends import demo_backend
from faq_ai.config import Settings
from faq_ai.models import Scope
from faq_ai.rag import RagService
from faq_ai.store import Store


def run_demo(temp_parent: Path) -> dict:
    """Exercise the actual pipeline with explicitly non-LLM fixtures."""
    temp_parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="faq-demo-", dir=temp_parent) as directory:
        root = Path(directory)
        first = root / "north.md"
        second = root / "south.md"
        first.write_text("Доставка в Москву занимает 2 рабочих дня и стоит 500 рублей.\n\n"
                         "Самовывоз доступен с 10:00 до 18:00.", encoding="utf-8")
        second.write_text("Доставка в Москву занимает 7 рабочих дней и стоит 900 рублей.", encoding="utf-8")
        store = Store(root / "demo.sqlite3")
        service = RagService(store, demo_backend(), Settings(min_score=0.05))
        scope_a = Scope(account_id="company-a", agent_id="agent-a")
        scope_b = Scope(account_id="company-b", agent_id="agent-a")
        same_account_agent_b = Scope(account_id="company-a", agent_id="agent-b")
        try:
            service.ingest(scope_a, first, "delivery")
            service.ingest(scope_b, second, "delivery")
            service.ingest(same_account_agent_b, second, "delivery")
            known = service.ask(scope_a, "Сколько стоит доставка в Москву?")
            missing = service.ask(scope_a, "Кто открыл Плутон?")
            other_account = service.ask(scope_b, "Сколько стоит доставка в Москву?")
            other_agent = service.ask(same_account_agent_b, "Сколько стоит доставка в Москву?")
            first.write_text("Доставка в Москву занимает 3 рабочих дня и стоит 600 рублей.", encoding="utf-8")
            service.ingest(scope_a, first, "delivery")
            replaced = service.ask(scope_a, "Сколько стоит доставка в Москву?")
            store.delete(scope_a, "delivery")
            deleted = service.ask(scope_a, "Сколько стоит доставка в Москву?")
            checks = {
                "known_answer_with_source": known.has_answer and "500" in known.answer and bool(known.sources),
                "missing_no_answer": not missing.has_answer,
                "account_isolation": "900" in other_account.answer and "900" not in known.answer,
                "agent_isolation": "900" in other_agent.answer and "900" not in known.answer,
                "replacement": "600" in replaced.answer and "500" not in replaced.answer,
                "deletion": not deleted.has_answer and deleted.reason == "empty_knowledge",
            }
            return {
                "mode": "demo-extractive: no LLM, token hashes instead of semantic embeddings",
                "checks": checks, "passed": all(checks.values()),
                "examples": {"known": known.model_dump(), "missing": missing.model_dump(),
                             "replaced": replaced.model_dump(), "deleted": deleted.model_dump()},
            }
        finally:
            store.close()
