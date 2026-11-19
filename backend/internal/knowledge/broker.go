package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"faqbot/backend/internal/messaging"
	amqp "github.com/rabbitmq/amqp091-go"
)

type Broker struct {
	Connection *amqp.Connection
	Channel    *amqp.Channel
	returns    <-chan amqp.Return
}

func Dial(url string) (*Broker, error) {
	connection, err := amqp.DialConfig(url, amqp.Config{Heartbeat: 10 * time.Second, Dial: amqp.DefaultDial(5 * time.Second)})
	if err != nil {
		return nil, errors.New("AMQP connect failed")
	}
	channel, err := connection.Channel()
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	for _, exchange := range []string{"faq.jobs", "faq.job-results"} {
		if err = channel.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
			_ = connection.Close()
			return nil, err
		}
	}
	for _, q := range []string{"faq.ai.documents", "faq.go.results"} {
		if _, err = channel.QueueDeclare(q, true, false, false, false, nil); err != nil {
			_ = connection.Close()
			return nil, err
		}
	}
	for _, key := range []string{"document.index", "document.delete", "document.reindex"} {
		if err = channel.QueueBind("faq.ai.documents", key, "faq.jobs", false, nil); err != nil {
			_ = connection.Close()
			return nil, err
		}
	}
	if err = channel.QueueBind("faq.go.results", "jobs.*", "faq.job-results", false, nil); err != nil {
		_ = connection.Close()
		return nil, err
	}
	if err = channel.Confirm(false); err != nil {
		_ = connection.Close()
		return nil, err
	}
	if err = channel.Qos(1, 0, false); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return &Broker{connection, channel, channel.NotifyReturn(make(chan amqp.Return, 1))}, nil
}
func (b *Broker) Close() { _ = b.Channel.Close(); _ = b.Connection.Close() }

func (b *Broker) Publish(ctx context.Context, exchange, key string, body []byte, id string) error {
	returned := b.returns
	confirmation, err := b.Channel.PublishWithDeferredConfirmWithContext(ctx, exchange, key, true, false, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: id, Body: body})
	if err != nil {
		return err
	}
	confirmed, err := confirmation.WaitContext(ctx)
	if err != nil {
		return err
	}
	if !confirmed {
		return errors.New("AMQP publish nack")
	}
	select {
	case <-returned:
		return errors.New("AMQP unroutable publish")
	default:
		return nil
	}
}

func (s Store) Dispatch(ctx context.Context, b *Broker) error {
	// DB outbox survives crashes between publishing and marking delivered. Repeats
	// are expected and handled by the worker's durable receipt.
	rows, err := s.DB.QueryContext(ctx, `SELECT job_id::text,payload FROM knowledge.outbox WHERE published_at IS NULL ORDER BY job_id LIMIT 100`)
	if err != nil {
		return err
	}
	type item struct {
		id   string
		body []byte
	}
	items := []item{}
	for rows.Next() {
		var in item
		if err = rows.Scan(&in.id, &in.body); err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, in)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, in := range items {
		var job messaging.Message
		if err = json.Unmarshal(in.body, &job); err != nil {
			return err
		}
		if err = b.Publish(ctx, "faq.jobs", job.EventType, in.body, in.id); err != nil {
			return err
		}
		if _, err = s.DB.ExecContext(ctx, `UPDATE knowledge.outbox SET published_at=now() WHERE job_id=$1`, in.id); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) ConsumeResult(ctx context.Context, b *Broker) (bool, error) {
	delivery, ok, err := b.Channel.Get("faq.go.results", false)
	if err != nil || !ok {
		return false, err
	}
	var result messaging.Message
	if len(delivery.Body) > 64000 || json.Unmarshal(delivery.Body, &result) != nil {
		return true, delivery.Reject(false)
	}
	if err = s.ApplyResult(ctx, result); err != nil {
		if errors.Is(err, ErrStale) || errors.Is(err, ErrConflict) || errors.Is(err, sql.ErrNoRows) {
			return true, delivery.Reject(false)
		}
		_ = delivery.Nack(false, true)
		return true, err
	}
	return true, delivery.Ack(false)
}
