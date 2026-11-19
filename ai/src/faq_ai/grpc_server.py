"""Private, authenticated unary service. No browser-provided scope is accepted here."""
import concurrent.futures
import hmac
import signal
import threading
from uuid import UUID

import grpc
import psycopg
from grpc_health.v1 import health, health_pb2, health_pb2_grpc

from faq_ai.config import Settings
from faq_ai.generated import ai_pb2, ai_pb2_grpc
from faq_ai.jobs import JobError
from faq_ai.models import AppError, Scope
from faq_ai.pg_store import PgStore
from faq_ai.rag import RagService
from faq_ai.runtime import runtime
from faq_ai.service_config import ServiceSettings


class AIService(ai_pb2_grpc.AIServiceServicer):
    def __init__(self, store, backend, settings, config):
        self.store, self.backend, self.settings, self.config = store, backend, settings, config
        self.gate = threading.BoundedSemaphore(config.concurrency)

    def GenerateAnswer(self, request, context):
        metadata = dict(context.invocation_metadata())
        expected = "Bearer " + self.config.service_token.get_secret_value()
        if not hmac.compare_digest(metadata.get("authorization", ""), expected):
            context.abort(grpc.StatusCode.UNAUTHENTICATED, "service_credential_required")
        try:
            UUID(request.account_id)
            UUID(request.agent_id)
            if not request.request_id or len(request.request_id) > 128 or len(request.trace_id) > 128:
                raise ValueError()
            if request.conversation_id:
                UUID(request.conversation_id)
            if not request.question.strip() or len(request.question) > 2000 or len(request.history) > 20:
                raise ValueError()
            if request.HasField("knowledge_revision") and request.knowledge_revision < 0:
                raise ValueError()
        except ValueError:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "invalid_request")
        if request.history:
            context.abort(grpc.StatusCode.UNIMPLEMENTED, "history_requires_ai_020")
        if context.time_remaining() is None or context.time_remaining() > self.config.rpc_timeout + 1:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "deadline_required")
        if not self.gate.acquire(blocking=False):
            context.abort(grpc.StatusCode.RESOURCE_EXHAUSTED, "generation_busy")
        try:
            if not context.is_active():
                context.abort(grpc.StatusCode.CANCELLED, "cancelled")
            scope = Scope(account_id=request.account_id, agent_id=request.agent_id)
            if request.HasField("knowledge_revision") and self.store.revision(scope) != request.knowledge_revision:
                context.abort(grpc.StatusCode.FAILED_PRECONDITION, "knowledge_changed")
            answer = RagService(self.store, self.backend, self.settings).ask(scope, request.question)
            if not context.is_active():
                context.abort(grpc.StatusCode.DEADLINE_EXCEEDED, "deadline_exceeded")
            # Source versions/provenance are read from the same active metadata snapshot.
            _, chunks = self.store.snapshot(scope)
            by_id = {c["chunk_id"]: c for c in chunks}
            if self.store.revision(scope) != answer.knowledge_version:
                context.abort(grpc.StatusCode.FAILED_PRECONDITION, "knowledge_changed")
            sources = []
            for source in answer.sources:
                chunk = by_id.get(source.chunk_id)
                if chunk is None:
                    context.abort(grpc.StatusCode.FAILED_PRECONDITION, "knowledge_changed")
                sources.append(ai_pb2.Source(document_id=source.document_id, document_version=chunk["document_version"],
                    chunk_id=source.chunk_id, display_name=source.filename, excerpt=source.quote, page=chunk["page"],
                    heading=chunk["heading"], start_char=source.start_char, score=source.score))
            usage = ai_pb2.Usage(available=answer.usage_available)
            if answer.usage_available:
                usage.input_tokens, usage.output_tokens = answer.input_tokens, answer.output_tokens
            return ai_pb2.GenerateAnswerResponse(request_id=request.request_id, answer=answer.answer,
                has_answer=answer.has_answer, reason=answer.reason, sources=sources,
                knowledge_revision=answer.knowledge_version, usage=usage, generation_model=answer.generation_model,
                embedding_model=answer.embedding_model, prompt_version=answer.prompt_version)
        except psycopg.Error:
            context.abort(grpc.StatusCode.UNAVAILABLE, "database_unavailable")
        except AppError as error:
            status = grpc.StatusCode.FAILED_PRECONDITION if error.code in {"stale_index", "knowledge_changed"} else grpc.StatusCode.UNAVAILABLE
            context.abort(status, error.code)
        except JobError as error:
            context.abort(grpc.StatusCode.UNAVAILABLE, error.code)
        finally:
            self.gate.release()


def create_server(settings, config, store=None, backend=None):
    store = store or PgStore(config.database_url.get_secret_value())
    backend = backend or runtime(settings, config)
    server = grpc.server(concurrent.futures.ThreadPoolExecutor(max_workers=config.concurrency + config.queue_size + 1),
                         maximum_concurrent_rpcs=config.concurrency + config.queue_size,
                         options=(("grpc.max_receive_message_length", 64_000), ("grpc.max_send_message_length", 128_000)))
    ai_pb2_grpc.add_AIServiceServicer_to_server(AIService(store, backend, settings, config), server)
    probe = health.HealthServicer()
    health_pb2_grpc.add_HealthServicer_to_server(probe, server)
    probe.set("faq.ai.v1.AIService", health_pb2.HealthCheckResponse.SERVING)
    if not server.add_insecure_port(config.grpc_listen):
        raise RuntimeError("gRPC listen failed")
    return server


def main():
    settings, config = Settings.from_env(), ServiceSettings.from_env()
    server = create_server(settings, config)
    def stop(*_):
        server.stop(10)
    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)
    server.start()
    server.wait_for_termination()


if __name__ == "__main__":
    main()
