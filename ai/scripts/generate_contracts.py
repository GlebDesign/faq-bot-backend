"""One canonical proto; pinned generators; reproducible Python/Go and wire fixtures."""
import argparse
import base64
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

from faq_ai.jobs import Job

root=Path(__file__).resolve().parents[2]
parser=argparse.ArgumentParser()
parser.add_argument('--check',action='store_true',help='Fail if checked-in generated files differ')
args=parser.parse_args()
suffix='.exe' if os.name=='nt' else ''
tools=root/'ai'/'.runtime'/'tools'
plugins={name:(tools/(name+suffix)) if (tools/(name+suffix)).exists() else Path(shutil.which(name) or name)
         for name in ('protoc-gen-go','protoc-gen-go-grpc')}
for name,version in [('protoc-gen-go','v1.36.6'),('protoc-gen-go-grpc','1.5.1')]:
    if version not in subprocess.check_output([str(plugins[name]),'--version'],text=True):
        raise RuntimeError('Unexpected generator version: '+name)
proto=root/'contracts'/'proto'/'faq'/'ai'/'v1'
with tempfile.TemporaryDirectory() as temp:
    out=Path(temp);py=out/'python';go=out/'go';py.mkdir();go.mkdir()
    subprocess.run([sys.executable,'-m','grpc_tools.protoc','-I',str(proto),
        '--python_out='+str(py),'--grpc_python_out='+str(py),
        '--plugin=protoc-gen-go='+str(plugins['protoc-gen-go']),
        '--plugin=protoc-gen-go-grpc='+str(plugins['protoc-gen-go-grpc']),
        '--go_out='+str(go),'--go_opt=paths=source_relative',
        '--go-grpc_out='+str(go),'--go-grpc_opt=paths=source_relative',str(proto/'ai.proto')],check=True)
    grpc_path=py/'ai_pb2_grpc.py'
    grpc_path.write_text(grpc_path.read_text().replace('import ai_pb2 as ai__pb2','from . import ai_pb2 as ai__pb2'))
    for origin,target in [(py,root/'ai'/'src'/'faq_ai'/'generated'),(go,root/'backend'/'internal'/'aipb')]:
        target.mkdir(parents=True,exist_ok=True)
        for file in origin.iterdir():
            destination=target/file.name
            if args.check:
                if not destination.exists() or destination.read_text(encoding='utf-8')!=file.read_text(encoding='utf-8'):
                    raise RuntimeError('Generated file differs: '+str(destination.relative_to(root)))
            else:
                shutil.copyfile(file,destination)
from faq_ai.generated import ai_pb2
request=ai_pb2.GenerateAnswerRequest(request_id='wire-request-1',trace_id='wire-trace-1',
    account_id='00000000-0000-4000-8000-000000000001',agent_id='00000000-0000-4000-8000-000000000003',
    question='Сколько стоит доставка в Москву?',knowledge_revision=7)
job=Job(schema_version=1,event_type='document.index',job_id='00000000-0000-4000-8000-000000000010',
    operation_id='00000000-0000-4000-8000-000000000011',account_id=request.account_id,agent_id=request.agent_id,
    document_id='00000000-0000-4000-8000-000000000012',document_version=1,knowledge_revision=7,
    request_id=request.request_id,trace_id=request.trace_id,attempt=1)
fixtures={'question':request.question,'request_base64':base64.b64encode(request.SerializeToString(deterministic=True)).decode(),
          'job':job.model_dump(mode='json')}
directory=root/'contracts'/'ai';directory.mkdir(parents=True,exist_ok=True)
for name,data in [('fixtures.json',fixtures),('job.schema.json',Job.model_json_schema())]:
    path=directory/name;text=json.dumps(data,ensure_ascii=False,indent=2)+'\n'
    if args.check:
        if path.read_text(encoding='utf-8')!=text:raise RuntimeError('Fixture differs: '+name)
    else:path.write_text(text,encoding='utf-8')
print('Canonical Python/Go contract and fixtures '+('verified' if args.check else 'generated'))
