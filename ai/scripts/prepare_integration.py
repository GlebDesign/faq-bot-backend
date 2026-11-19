"""Provision only the explicitly named local integration database's runtime role."""
import os
from pathlib import Path

import psycopg
from psycopg import sql


def environment():
    path=Path(__file__).resolve().parents[1]/'.runtime'/'integration.env'
    return dict(line.split('=',1) for line in path.read_text(encoding='utf-8-sig').splitlines() if line and not line.startswith('#'))


def main():
    values=environment()
    with psycopg.connect(values['DATABASE_URL'],autocommit=True) as conn:
        if conn.execute('SELECT current_database()').fetchone()[0]!='faq_ai18':
            raise RuntimeError('This helper only provisions the faq_ai18 test database')
        if not conn.execute("SELECT 1 FROM pg_roles WHERE rolname='faq_ai18'").fetchone():
            conn.execute('CREATE ROLE faq_ai18 LOGIN')
        conn.execute(sql.SQL('ALTER ROLE faq_ai18 PASSWORD {}').format(sql.Literal(values['AI_TEST_RUNTIME_PASSWORD'])))
        conn.execute('GRANT faq_ai_runtime TO faq_ai18')
        if not conn.execute("SELECT 1 FROM pg_roles WHERE rolname='faq_go18'").fetchone():
            conn.execute('CREATE ROLE faq_go18 LOGIN')
        conn.execute(sql.SQL('ALTER ROLE faq_go18 PASSWORD {}').format(sql.Literal(values['AI_TEST_GO_PASSWORD'])))
        conn.execute('GRANT faq_go_knowledge_runtime TO faq_go18')
    print('Restricted integration role ready; no business-table write permission.')


if __name__=='__main__':
    main()
