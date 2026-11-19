import os
from pathlib import Path

import pytest

from prepare_integration import environment

values=environment()
os.environ['AI_TEST_ADMIN_URL']=values['DATABASE_URL']
os.environ['AI_DATABASE_URL']=values['AI_DATABASE_URL']
root=Path(__file__).resolve().parents[1]
raise SystemExit(pytest.main([str(root/'tests'),'-q','--tb=short','-p','no:cacheprovider',
                             '--basetemp',str(root/'.runtime'/'pytest-integration')]))
