"""Local integration launcher; demo is explicit and never selected on failure."""
import argparse
import json
import os
import runpy
from pathlib import Path

from prepare_integration import environment

parser=argparse.ArgumentParser()
parser.add_argument('mode',choices=['worker','grpc'])
parser.add_argument('--demo',action='store_true',help='Deterministic adapter for infrastructure tests only')
args=parser.parse_args()
root=Path(__file__).resolve().parents[1]
for key,value in environment().items():
    if key in {"AI_DATABASE_URL", "AI_AMQP_URL", "AI_SERVICE_TOKEN"}:
        os.environ.setdefault(key,value)
for key,value in json.loads((root/'local-models.json').read_text()).items():
    os.environ.setdefault(key,str(value))
os.environ.setdefault('AI_SOURCE_ROOT',str(root/'.runtime'/'integration'/'sources'))
os.environ.setdefault('AI_GRPC_LISTEN','127.0.0.1:50051')
os.environ['AI_BACKEND']='demo' if args.demo else 'ollama'
os.environ['AI_EMBEDDING_DIMENSION']='8192' if args.demo else '1024'
os.environ.setdefault('AI_EMBEDDING_DIGEST','ac6da0dfba84a81fdbfbaf330198c33cd77c4cdfc53e8bc50eb581914a15621d')
os.environ.setdefault('AI_GENERATION_DIGEST','0edcdef34593eac1aa2be9c7d06c432dcf81945adca5eca2f27662c18f168ba0')
runpy.run_module('faq_ai.'+('grpc_server' if args.mode=='grpc' else 'worker'),run_name='__main__')
