"""Real PostgreSQL/pgvector tests, isolated rows in the dedicated integration DB."""
import hashlib
import json
import os
from pathlib import Path
from uuid import uuid4

import psycopg
import pytest
from psycopg import sql
from psycopg.types.json import Jsonb

from faq_ai.backends import demo_backend
from faq_ai.config import Settings
from faq_ai.jobs import Job, JobError
from faq_ai.models import Scope
from faq_ai.pg_store import PgStore
from faq_ai.runtime import BoundedEmbeddings
from faq_ai.service_config import ServiceSettings
from faq_ai.worker import Processor

pytestmark=pytest.mark.integration


@pytest.fixture
def env(tmp_path):
    admin=os.getenv('AI_TEST_ADMIN_URL')
    ai=os.getenv('AI_DATABASE_URL')
    if not admin or not ai:
        pytest.skip('Set AI_TEST_ADMIN_URL and AI_DATABASE_URL for real PostgreSQL tests')
    account,agent,document=uuid4(),uuid4(),uuid4()
    with psycopg.connect(admin,autocommit=True) as conn:
        conn.execute('INSERT INTO accounts(id) VALUES(%s)',(account,))
        conn.execute('INSERT INTO agents(id,account_id,public_id,name) VALUES(%s,%s,%s,%s)',(agent,account,'test-'+agent.hex,'Тест'))
    cfg=ServiceSettings(database_url=ai,service_token='test-token-'+'x'*40,source_root=tmp_path,
                        backend='demo',embedding_dimension=8192,lease_seconds=30)
    backend=demo_backend()
    backend.embeddings=BoundedEmbeddings(backend.embeddings,8192,8,1,2)
    store=PgStore(ai)
    processor=Processor(store,backend,Settings(min_score=0.1),cfg)
    yield dict(admin=admin,account=account,agent=agent,document=document,root=tmp_path,
               config=cfg,store=store,processor=processor,backend=backend)
    with psycopg.connect(admin,autocommit=True) as conn:
        conn.execute('DELETE FROM ai.chunks WHERE generation_id IN(SELECT id FROM ai.generations WHERE account_id=%s)',(account,))
        conn.execute('DELETE FROM ai.generations WHERE account_id=%s',(account,))
        conn.execute('DELETE FROM ai.receipts WHERE job_id IN(SELECT id FROM knowledge.jobs WHERE account_id=%s)',(account,))
        conn.execute('DELETE FROM knowledge.outbox WHERE job_id IN(SELECT id FROM knowledge.jobs WHERE account_id=%s)',(account,))
        conn.execute('DELETE FROM knowledge.jobs WHERE account_id=%s',(account,))
        conn.execute('DELETE FROM knowledge.documents WHERE account_id=%s',(account,))
        conn.execute('DELETE FROM agents WHERE account_id=%s',(account,))
        conn.execute('DELETE FROM accounts WHERE id=%s',(account,))


def seed(env,kind='document.index',version=1,text='Доставка в Москву стоит 500 рублей.',fmt='txt',raw=None):
    job=Job(schema_version=1,event_type=kind,job_id=uuid4(),operation_id=uuid4(),
        account_id=env['account'],agent_id=env['agent'],document_id=env['document'],
        document_version=version,knowledge_revision=version,attempt=1,request_id='pytest',trace_id='test')
    raw=raw if raw is not None else text.encode()
    key=f'{version}.{fmt}'
    (env['root']/key).write_bytes(raw)
    with psycopg.connect(env['admin'],autocommit=True) as conn:
        conn.execute("""INSERT INTO knowledge.documents(id,account_id,agent_id,name,format,storage_key,source_sha256,
            current_version,status,current_job_id,knowledge_revision,deleted_at)
            VALUES(%s,%s,%s,'Условия',%s,%s,%s,%s,%s,%s,%s,CASE WHEN %s THEN now() ELSE NULL END)
            ON CONFLICT(id) DO UPDATE SET current_version=excluded.current_version,current_job_id=excluded.current_job_id,
            status=excluded.status,format=excluded.format,storage_key=excluded.storage_key,
            source_sha256=excluded.source_sha256,knowledge_revision=excluded.knowledge_revision,deleted_at=excluded.deleted_at""",
            (job.document_id,job.account_id,job.agent_id,fmt,key,hashlib.sha256(raw).hexdigest(),version,
             'deleting' if kind=='document.delete' else 'processing',job.job_id,version,kind=='document.delete'))
        conn.execute('INSERT INTO knowledge.jobs(id,operation_id,account_id,agent_id,document_id,submission_hash,payload) VALUES(%s,%s,%s,%s,%s,%s,%s)',
                     (job.job_id,job.operation_id,job.account_id,job.agent_id,job.document_id,b'hash',Jsonb(job.model_dump(mode='json'))))
    return job


def activate(env,job):
    with psycopg.connect(env['admin'],autocommit=True) as conn:
        conn.execute("UPDATE knowledge.documents SET active_generation=%s,active_version=%s,status='ready' WHERE id=%s",(job.job_id,job.document_version,job.document_id))


def scope(env):return Scope(account_id=str(env['account']),agent_id=str(env['agent']))


def test_real_pg_staging_retry_activation_and_isolation(env):
    job=seed(env)
    first=env['processor'].process(job)
    assert first['status']=='completed'
    assert env['store'].snapshot(scope(env))[1]==[]  # Pending data is invisible.
    original=env['backend'].embeddings
    env['backend'].embeddings=None  # Cached receipt must not call the model again.
    assert env['processor'].process(job)==first
    env['backend'].embeddings=original
    activate(env,job)
    revision,chunks=env['store'].snapshot(scope(env))
    assert revision==1 and '500' in chunks[0]['content'] and chunks[0]['dimension']==8192
    assert env['store'].snapshot(Scope(account_id=str(uuid4()),agent_id=str(env['agent'])))[1]==[]
    assert env['store'].snapshot(Scope(account_id=str(env['account']),agent_id=str(uuid4())))[1]==[]


def test_delete_fences_late_index_and_repeat_is_safe(env):
    old=seed(env)
    deletion=seed(env,'document.delete',2)
    assert env['processor'].process(old)['error_code']=='stale_job'
    assert env['processor'].process(deletion)['status']=='completed'
    assert env['processor'].process(deletion)['status']=='completed'
    assert env['store'].snapshot(scope(env))[1]==[]


def test_failed_reindex_keeps_old_until_confirmed_activation_then_cleans(env):
    old=seed(env);env['processor'].process(old);activate(env,old)
    failed=seed(env,'document.reindex',2,raw=b'broken',fmt='pdf')
    assert env['processor'].process(failed)['error_code']=='corrupt_pdf'
    assert '500' in env['store'].snapshot(scope(env))[1][0]['content']
    fresh=seed(env,'document.reindex',3,text='Доставка в Москву стоит 600 рублей.')
    env['processor'].process(fresh)
    env['store'].cleanup()
    assert '500' in env['store'].snapshot(scope(env))[1][0]['content']
    activate(env,fresh);env['store'].cleanup()
    assert '600' in env['store'].snapshot(scope(env))[1][0]['content']
    with psycopg.connect(env['admin']) as conn:
        assert conn.execute('SELECT count(*) FROM ai.generations WHERE account_id=%s',(env['account'],)).fetchone()[0]==1


def test_expired_lease_fencing_prevents_old_owner_commit(env):
    job=seed(env);first,second=uuid4(),uuid4()
    fence,_=env['store'].claim(job,first,30)
    with psycopg.connect(env['admin'],autocommit=True) as conn:
        conn.execute("UPDATE ai.receipts SET lease_until=now()-interval '1 second' WHERE job_id=%s",(job.job_id,))
    new,_=env['store'].claim(job,second,30)
    assert new>fence
    with pytest.raises(JobError) as error:
        env['store'].finish(job,first,fence,job.result('failure'))
    assert error.value.code=='lease_lost'
    env['store'].finish(job,second,new,job.result('new_owner'))


def test_bad_dimension_rolls_back_entire_generation(env):
    job=seed(env);owner=uuid4();fence,_=env['store'].claim(job,owner,30)
    chunk=dict(number=0,chunk_id='one',content='Доставка',page=0,heading='',start_char=0)
    with pytest.raises(JobError):
        env['store'].finish(job,owner,fence,job.result(count=1),[chunk],[[1.]],'model',2)
    with psycopg.connect(env['admin']) as conn:
        assert conn.execute('SELECT count(*) FROM ai.generations WHERE id=%s',(job.job_id,)).fetchone()[0]==0
        assert conn.execute('SELECT result FROM ai.receipts WHERE job_id=%s',(job.job_id,)).fetchone()[0] is None


def test_runtime_role_cannot_write_go_metadata(env):
    with env['store'].connect() as conn:
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            conn.execute("UPDATE knowledge.documents SET status='ready'")


def test_delete_during_embeddings_fences_inflight_index(env,monkeypatch):
    job=seed(env)
    original=env['backend'].embeddings.embed_documents
    def racing(texts):
        vectors=original(texts)
        seed(env,'document.delete',2)
        return vectors
    monkeypatch.setattr(env['backend'].embeddings,'embed_documents',racing)
    result=env['processor'].process(job)
    assert result['status']=='failed' and result['error_code']=='stale_job'
    with psycopg.connect(env['admin']) as conn:
        assert conn.execute('SELECT count(*) FROM ai.generations WHERE document_id=%s',(job.document_id,)).fetchone()[0]==0


def test_model_revision_activation_never_mixes_generations(env):
    old=seed(env);env['processor'].process(old);activate(env,old)
    old_key=env['backend'].embedding_key
    env['backend'].embedding_key='test-new-model-fingerprint'
    fresh=seed(env,'document.reindex',2,text='Доставка стоит 600 рублей.')
    env['processor'].process(fresh)
    assert {c['embedding_key'] for c in env['store'].snapshot(scope(env))[1]}=={old_key}
    activate(env,fresh)
    chunks=env['store'].snapshot(scope(env))[1]
    assert {c['embedding_key'] for c in chunks}=={'test-new-model-fingerprint'}
    assert {c['document_version'] for c in chunks}=={2}
