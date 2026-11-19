import base64
import json
from pathlib import Path

from faq_ai.generated import ai_pb2
from faq_ai.jobs import Job


def test_canonical_wire_fixture_and_queue_schema():
    root=Path(__file__).resolve().parents[2]
    fixture=json.loads((root/'contracts'/'ai'/'fixtures.json').read_text(encoding='utf-8'))
    request=ai_pb2.GenerateAnswerRequest.FromString(base64.b64decode(fixture['request_base64']))
    assert request.question==fixture['question'] and request.knowledge_revision==7
    assert request.HasField('knowledge_revision')
    job=Job.model_validate(fixture['job'])
    assert str(job.account_id)==request.account_id and str(job.agent_id)==request.agent_id
    assert job.schema_version==1
