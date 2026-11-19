import json
from concurrent.futures import ThreadPoolExecutor
from types import SimpleNamespace
from uuid import uuid4

import pytest

from faq_ai.jobs import Job,JobError
from faq_ai.worker import Worker


def message():
    return Job(schema_version=1,event_type='document.index',job_id=uuid4(),operation_id=uuid4(),
        account_id=uuid4(),agent_id=uuid4(),document_id=uuid4(),document_version=1,
        knowledge_revision=1,request_id='r',trace_id='t',attempt=1)


class Channel:
    is_open=True
    def __init__(self,events,fail=False):self.events,self.fail=events,fail
    def basic_publish(self,exchange,key,body,**kwargs):
        assert kwargs['mandatory'] and kwargs['properties'].delivery_mode==2
        self.events.append('publish:'+exchange)
        if self.fail:raise ConnectionError('publisher confirm failed')
    def basic_ack(self,tag):self.events.append('ack')
    def basic_nack(self,tag,**kwargs):self.events.append('nack')


def deliver(processor,events,body,fail=False,attempt=1):
    worker=Worker(SimpleNamespace(max_attempts=3),processor)
    channel=Channel(events,fail)
    connection=SimpleNamespace(process_data_events=lambda **kwargs:None)
    with ThreadPoolExecutor(max_workers=1) as executor:
        worker.deliver(connection,channel,SimpleNamespace(delivery_tag=1),
                       SimpleNamespace(headers={'x-faq-attempt':attempt}),body,executor)


def test_ack_follows_committed_receipt_confirm_and_publication_mark():
    events=[];job=message()
    def effect(*args,**kwargs):events.append('commit');return job.result(count=1)
    store=SimpleNamespace(published=lambda job:events.append('published'))
    deliver(SimpleNamespace(process=effect,store=store),events,job.model_dump_json().encode())
    assert events==['commit','publish:faq.job-results','published','ack']


def test_lost_publish_never_acks_committed_input():
    events=[];job=message()
    def effect(*args,**kwargs):events.append('commit');return job.result(count=1)
    with pytest.raises(ConnectionError):
        deliver(SimpleNamespace(process=effect),events,job.model_dump_json().encode(),fail=True)
    assert events==['commit','publish:faq.job-results','nack']


def test_transient_failure_schedules_confirmed_retry_before_ack():
    events=[];job=message()
    def temporary(*args,**kwargs):raise JobError('embedding_unavailable',True)
    deliver(SimpleNamespace(process=temporary),events,job.model_dump_json().encode())
    assert events==['publish:faq.ai.retry','ack']


def test_retry_exhaustion_publishes_failure_and_dlq_before_ack():
    events=[];job=message()
    def temporary(*args,**kwargs):
        if kwargs.get('terminal_error'):
            events.append('failed_receipt_commit');return job.result(kwargs['terminal_error'])
        raise JobError('embedding_unavailable',True)
    store=SimpleNamespace(published=lambda job:events.append('published'))
    deliver(SimpleNamespace(process=temporary,store=store),events,job.model_dump_json().encode(),attempt=3)
    assert events==['failed_receipt_commit','publish:faq.job-results','published','publish:faq.ai.dead','ack']


def test_unsupported_schema_deadletters_once_without_effect():
    events=[];data=message().model_dump(mode='json');data['schema_version']=99
    deliver(None,events,json.dumps(data).encode())
    assert events==['publish:faq.ai.dead','ack']
