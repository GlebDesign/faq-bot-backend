"""Durable effects -> confirmed result -> input ACK; no writes to Go-owned tables."""
import concurrent.futures
import json
import logging
import signal
import threading
import time
from uuid import uuid4

import pika
import psycopg
from pydantic import ValidationError

from faq_ai.config import Settings
from faq_ai.jobs import Job, JobError
from faq_ai.parsers import chunks_for, parse_isolated, read_source
from faq_ai.pg_store import PgStore
from faq_ai.runtime import runtime
from faq_ai.service_config import ServiceSettings

LOG = logging.getLogger("faq_ai.worker")
MAX_MESSAGE = 64_000


def topology(channel, retry_seconds):
    for exchange in ("faq.jobs", "faq.job-results", "faq.ai.retry", "faq.ai.dead"):
        channel.exchange_declare(exchange, "topic", durable=True)
    channel.queue_declare("faq.ai.documents", durable=True)
    for key in ("document.index", "document.delete", "document.reindex"):
        channel.queue_bind("faq.ai.documents", "faq.jobs", routing_key=key)
    channel.queue_declare("faq.go.results", durable=True)
    channel.queue_bind("faq.go.results", "faq.job-results", routing_key="jobs.*")
    channel.queue_declare("faq.ai.retry", durable=True, arguments={
        "x-message-ttl": retry_seconds * 1000, "x-dead-letter-exchange": "faq.jobs"})
    channel.queue_bind("faq.ai.retry", "faq.ai.retry", routing_key="#")
    channel.queue_declare("faq.ai.dead", durable=True)
    channel.queue_bind("faq.ai.dead", "faq.ai.dead", routing_key="#")
    channel.confirm_delivery()


class Processor:
    def __init__(self, store, backend, settings, service):
        self.store, self.backend, self.settings, self.service = store, backend, settings, service

    def process(self, job, terminal_error=None):
        owner = uuid4()
        fence, cached = self.store.claim(job, owner, self.service.lease_seconds)
        if cached is not None:
            return cached
        stopped = threading.Event()
        lease_errors = []

        def heartbeat():
            while not stopped.wait(self.service.lease_seconds / 3):
                try:
                    self.store.renew(job, owner, fence, self.service.lease_seconds)
                except Exception as exc:
                    lease_errors.append(exc)
                    return

        beat = threading.Thread(target=heartbeat, daemon=True)
        beat.start()
        try:
            if terminal_error:
                return self.store.finish(job, owner, fence, job.result(terminal_error))
            try:
                meta = self.store.metadata(job)
                if job.event_type == "document.delete":
                    return self.store.finish(job, owner, fence, job.result())
                raw = read_source(self.service.source_root, meta["storage_key"], meta["source_sha256"],
                                  self.service.max_file_bytes)
                sections = parse_isolated(raw, meta["format"], self.service.parse_timeout)
                chunks = chunks_for(sections, str(job.document_id), job.document_version,
                                    self.settings.chunk_size, self.settings.chunk_overlap)
                vectors = self.backend.embeddings.embed_documents([c["content"] for c in chunks])
                if lease_errors:
                    raise JobError("lease_lost", transient=True)
                return self.store.finish(job, owner, fence, job.result(count=len(chunks)), chunks, vectors,
                                         self.backend.embedding_key, self.service.embedding_dimension)
            except JobError as error:
                if error.transient:
                    raise
                return self.store.finish(job, owner, fence, job.result(error.code))
        except Exception:
            self.store.release(job, owner, fence)
            raise
        finally:
            stopped.set()
            beat.join(timeout=2)


class Worker:
    def __init__(self, config, processor):
        self.config, self.processor, self.stopping = config, processor, False

    def publish(self, channel, exchange, key, body, job_id=None, headers=None):
        channel.basic_publish(exchange, key, body, mandatory=True, properties=pika.BasicProperties(
            delivery_mode=2, content_type="application/json", message_id=job_id, headers=headers or {}))

    def deliver(self, connection, channel, method, properties, body, executor):
        try:
            if len(body) > MAX_MESSAGE:
                raise ValueError("message_size")
            job = Job.model_validate_json(body)
        except (ValueError, ValidationError):
            self.publish(channel, "faq.ai.dead", "invalid_message", body[:MAX_MESSAGE],
                         headers={"error_code": "invalid_message"})
            channel.basic_ack(method.delivery_tag)
            return
        try:
            attempt = int((properties.headers or {}).get("x-faq-attempt", 1))
            if not 1 <= attempt <= self.config.max_attempts:
                attempt = self.config.max_attempts
        except (TypeError, ValueError):
            attempt = self.config.max_attempts
        future = executor.submit(self.processor.process, job)
        try:
            while not future.done():
                # All AMQP calls stay on their owning thread, even during slow inference.
                connection.process_data_events(time_limit=0.2)
            try:
                result = future.result()
            except (JobError, psycopg.Error) as error:
                transient = isinstance(error, psycopg.Error) or error.transient
                if transient and attempt < self.config.max_attempts:
                    self.publish(channel, "faq.ai.retry", job.event_type, body, str(job.job_id),
                                 {"x-faq-attempt": attempt + 1})
                    channel.basic_ack(method.delivery_tag)
                    return
                code = error.code if isinstance(error, JobError) else "database_unavailable"
                result = self.processor.process(job, terminal_error=code)
            encoded = json.dumps(result, ensure_ascii=False).encode()
            key = "jobs.completed" if result["status"] == "completed" else "jobs.failed"
            self.publish(channel, "faq.job-results", key, encoded, str(job.job_id))
            self.processor.store.published(job)
            if result["status"] == "failed":
                self.publish(channel, "faq.ai.dead", job.event_type, body, str(job.job_id),
                             {"error_code": result["error_code"]})
            channel.basic_ack(method.delivery_tag)
        except Exception:
            # No ACK: close/reconnect lets the broker redeliver. A committed receipt
            # resends its saved result instead of repeating parsing/embeddings/deletion.
            if channel.is_open:
                channel.basic_nack(method.delivery_tag, requeue=True)
            raise

    def run(self):
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as executor:
            while not self.stopping:
                connection = None
                try:
                    parameters = pika.URLParameters(self.config.amqp_url.get_secret_value())
                    parameters.heartbeat = 60
                    parameters.blocked_connection_timeout = 15
                    parameters.socket_timeout = 5
                    connection = pika.BlockingConnection(parameters)
                    channel = connection.channel()
                    topology(channel, self.config.retry_seconds)
                    channel.basic_qos(prefetch_count=self.config.prefetch)
                    last_cleanup = 0
                    for method, properties, body in channel.consume("faq.ai.documents", inactivity_timeout=1):
                        if self.stopping:
                            break
                        if method:
                            self.deliver(connection, channel, method, properties, body, executor)
                        if time.monotonic() - last_cleanup > 5:
                            self.processor.store.cleanup()
                            last_cleanup = time.monotonic()
                except Exception as exc:
                    # No credentials, prompt, paths or source text in logs.
                    LOG.warning("worker reconnect: %s", type(exc).__name__)
                    for _ in range(20):
                        if self.stopping:
                            break
                        time.sleep(0.1)
                finally:
                    if connection and connection.is_open:
                        connection.close()


def main():
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(name)s %(message)s")
    settings, service = Settings.from_env(), ServiceSettings.from_env()
    worker = Worker(service, Processor(PgStore(service.database_url.get_secret_value()),
                                      runtime(settings, service), settings, service))
    def stop(*_):
        worker.stopping = True
    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)
    worker.run()


if __name__ == "__main__":
    main()
