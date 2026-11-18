import argparse
import importlib.metadata
import json
import os
import sqlite3
import sys
from pathlib import Path
from urllib.request import urlopen

from pydantic import ValidationError

from faq_ai.backends import ollama_backend
from faq_ai.config import Settings
from faq_ai.demo import run_demo
from faq_ai.models import AppError, Scope
from faq_ai.rag import RagService, validate_id
from faq_ai.store import Store


def emit(value):
    print(json.dumps(value, ensure_ascii=False, indent=2))


def doctor(settings: Settings) -> dict:
    report = {"python": sys.version.split()[0], "packages": {
        name: importlib.metadata.version(name) for name in
        ("langchain-core", "langchain-ollama", "langchain-text-splitters")
    }, "ollama_reachable": False, "models": [], "ready": False}
    try:
        with urlopen(settings.ollama_base_url.rstrip("/") + "/api/tags", timeout=min(settings.timeout, 5)) as response:
            data = json.load(response)
        report["ollama_reachable"] = True
        report["models"] = [m["name"] for m in data.get("models", [])]
        report["configured_models_present"] = {
            "generation": bool(settings.llm_model) and settings.llm_model in report["models"],
            "embedding": bool(settings.embedding_model) and settings.embedding_model in report["models"],
        }
        report["ready"] = all(report["configured_models_present"].values())
        report["note"] = "Проверено наличие тегов; inference и поддержка JSON-schema требуют отдельного прогона."
    except (OSError, ValueError, KeyError):
        report["error"] = "Ollama не отвечает или вернул неверный список моделей."
    return report


def parser():
    root = argparse.ArgumentParser(description="Локальный FAQ RAG на LangChain. Учебный CLI, без авторизации.")
    root.add_argument("--db", type=Path, default=Path("data/knowledge.sqlite3"))
    commands = root.add_subparsers(dest="command", required=True)
    commands.add_parser("demo", help="Автономная проверка на детерминированных заменах моделей")
    commands.add_parser("doctor", help="Проверить окружение и наличие моделей в Ollama")
    for name in ("ingest", "ask", "delete", "status"):
        command = commands.add_parser(name)
        command.add_argument("--account", required=True)
        command.add_argument("--agent", required=True)
        if name in {"ingest", "delete"}:
            command.add_argument("--document", required=True, help="Стабильный ID документа для замены/удаления")
        if name == "ingest":
            command.add_argument("file", type=Path)
        if name == "ask":
            command.add_argument("question")
    return root


def main(argv=None) -> int:
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8")
    args = parser().parse_args(argv)
    try:
        # Avoid sending documents to inherited remote tracing integrations.
        if any(os.getenv(key, "").lower() in {"true", "1"} for key in
               ("LANGCHAIN_TRACING_V2", "LANGSMITH_TRACING", "LANGCHAIN_TRACING")):
            raise AppError("configuration_error", "Отключи внешнюю трассировку LangSmith для локального прототипа.")
        settings = Settings.from_env()
        if args.command == "demo":
            report = run_demo(args.db.parent)
            emit(report)
            return 0 if report["passed"] else 1
        if args.command == "doctor":
            report = doctor(settings)
            emit(report)
            return 0 if report["ready"] else 1
        scope = Scope(account_id=args.account, agent_id=args.agent)
        validate_id(scope.account_id)
        validate_id(scope.agent_id)
        store = Store(args.db)
        try:
            if args.command == "status":
                emit(store.status(scope))
            elif args.command == "delete":
                validate_id(args.document)
                emit({"deleted": store.delete(scope, args.document), "knowledge_version": store.revision(scope)})
            else:
                service = RagService(store, ollama_backend(settings), settings)
                if args.command == "ingest":
                    emit(service.ingest(scope, args.file, args.document))
                else:
                    emit(service.ask(scope, args.question).model_dump())
        finally:
            store.close()
        return 0
    except AppError as exc:
        emit({"error": {"code": exc.code, "message": str(exc)}})
    except ValidationError as exc:
        emit({"error": {"code": "invalid_configuration_or_input", "fields": [
            ".".join(str(part) for part in e["loc"]) for e in exc.errors()
        ]}})
    except (OSError, sqlite3.Error):
        emit({"error": {"code": "storage_error", "message": "Не удалось открыть файл или локальный индекс."}})
    return 2
