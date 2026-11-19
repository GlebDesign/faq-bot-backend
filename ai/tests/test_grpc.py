from concurrent.futures import ThreadPoolExecutor

import grpc
import pytest

from faq_ai.backends import demo_backend
from faq_ai.config import Settings
from faq_ai.generated import ai_pb2, ai_pb2_grpc
from faq_ai.grpc_server import AIService
from faq_ai.service_config import ServiceSettings

ACCOUNT='00000000-0000-4000-8000-000000000001'
AGENT='00000000-0000-4000-8000-000000000003'
TOKEN='test-credential-'+'x'*40


@pytest.fixture
def rpc(tmp_path):
    class Store:
        def revision(self,scope):return 0
        def snapshot(self,scope):return 0,[]
    config=ServiceSettings(database_url='postgresql://unused',service_token=TOKEN,source_root=tmp_path)
    server=grpc.server(ThreadPoolExecutor(max_workers=3))
    ai_pb2_grpc.add_AIServiceServicer_to_server(AIService(Store(),demo_backend(),Settings(),config),server)
    port=server.add_insecure_port('127.0.0.1:0')
    server.start()
    with grpc.insecure_channel(f'127.0.0.1:{port}') as channel:
        yield ai_pb2_grpc.AIServiceStub(channel)
    server.stop(0).wait()


def request(**kwargs):
    data=dict(account_id=ACCOUNT,agent_id=AGENT,request_id='request-1',trace_id='trace-1',question='Доставка?')
    data.update(kwargs)
    return ai_pb2.GenerateAnswerRequest(**data)


def test_real_rpc_roundtrip_empty_knowledge(rpc):
    answer=rpc.GenerateAnswer(request(),timeout=2,metadata=[('authorization','Bearer '+TOKEN)])
    assert not answer.has_answer and answer.reason=='empty_knowledge'
    assert answer.request_id=='request-1'
    assert not answer.usage.available and not answer.usage.HasField('input_tokens')


@pytest.mark.parametrize('token', ['', 'Bearer wrong'])
def test_rpc_rejects_missing_or_wrong_credential(rpc,token):
    with pytest.raises(grpc.RpcError) as error:
        rpc.GenerateAnswer(request(),timeout=2,metadata=[('authorization',token)])
    assert error.value.code()==grpc.StatusCode.UNAUTHENTICATED


@pytest.mark.parametrize('changes,code', [
    ({'account_id':'other-from-prompt'},grpc.StatusCode.INVALID_ARGUMENT),
    ({'question':' '},grpc.StatusCode.INVALID_ARGUMENT),
    ({'knowledge_revision':1},grpc.StatusCode.FAILED_PRECONDITION),
    ({'history':[ai_pb2.HistoryMessage(role='user',text='old')]},grpc.StatusCode.UNIMPLEMENTED),
])
def test_rpc_request_and_version_errors(rpc,changes,code):
    with pytest.raises(grpc.RpcError) as error:
        rpc.GenerateAnswer(request(**changes),timeout=2,metadata=[('authorization','Bearer '+TOKEN)])
    assert error.value.code()==code


def test_rpc_rejects_missing_deadline(rpc):
    # gRPC exposes a very large remaining duration for calls without a deadline.
    with pytest.raises(grpc.RpcError) as error:
        rpc.GenerateAnswer(request(),metadata=[('authorization','Bearer '+TOKEN)])
    assert error.value.code()==grpc.StatusCode.INVALID_ARGUMENT
