"""Generate local-only random credentials. Never overwrite an existing environment."""
import secrets
from pathlib import Path

root = Path(__file__).resolve().parents[1] / ".runtime"
root.mkdir(exist_ok=True)
(root / "integration" / "sources").mkdir(parents=True, exist_ok=True)
path = root / "integration.env"
if path.exists():
    values = dict(line.split('=',1) for line in path.read_text(encoding='utf-8-sig').splitlines() if line)
    print("Integration environment already exists; credentials preserved.")
else:
    db_password, mq_password, ai_password, token = [secrets.token_hex(24) for _ in range(4)]
    values = {
        "AI_TEST_DB_PASSWORD": db_password, "AI_TEST_MQ_PASSWORD": mq_password,
        "AI_TEST_RUNTIME_PASSWORD": ai_password, "AI_SERVICE_TOKEN": token,
        "DATABASE_URL": f"postgresql://postgres:{db_password}@127.0.0.1:55432/faq_ai18?sslmode=disable",
        "AI_DATABASE_URL": f"postgresql://faq_ai18:{ai_password}@127.0.0.1:55432/faq_ai18?sslmode=disable",
        "AI_AMQP_URL": f"amqp://faq_ai18:{mq_password}@127.0.0.1:55672/ai18",
        "AI_CONTAINER_DATABASE_URL": f"postgresql://faq_ai18:{ai_password}@postgres:5432/faq_ai18?sslmode=disable",
        "AI_CONTAINER_AMQP_URL": f"amqp://faq_ai18:{mq_password}@rabbitmq:5672/ai18",
        "AI_GRPC_ADDR": "127.0.0.1:50051",
    }
    path.write_text("\n".join(f"{key}={value}" for key, value in values.items()) + "\n", encoding="utf-8")
    print("Random integration credentials generated in ignored ai/.runtime/integration.env")

go_password = values.setdefault('AI_TEST_GO_PASSWORD', secrets.token_hex(24))
values.setdefault('GO_CONTAINER_DATABASE_URL',f"postgresql://postgres:{values['AI_TEST_DB_PASSWORD']}@postgres:5432/faq_ai18?sslmode=disable")
values.setdefault('GO_RUNTIME_DATABASE_URL',f"postgresql://faq_go18:{go_password}@postgres:5432/faq_ai18?sslmode=disable")
path.write_text("\n".join(f"{key}={value}" for key,value in values.items())+"\n",encoding='utf-8')
