package aiclient

import (
	"context"
	"errors"
	"time"

	"faqbot/backend/internal/aipb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// Client is for trusted backend services; never copy account/agent from browser JSON.
type Client struct {
	connection *grpc.ClientConn
	stub       aipb.AIServiceClient
	token      string
	timeout    time.Duration
}

func New(address, token string, timeout time.Duration) (*Client, error) {
	if len(token) < 32 || timeout <= 0 || timeout > 5*time.Minute {
		return nil, errors.New("invalid AI client configuration")
	}
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(128000), grpc.MaxCallSendMsgSize(64000)))
	if err != nil {
		return nil, err
	}
	return &Client{connection, aipb.NewAIServiceClient(connection), token, timeout}, nil
}
func (c *Client) Close() error { return c.connection.Close() }
func (c *Client) Generate(ctx context.Context, request *aipb.GenerateAnswerRequest) (*aipb.GenerateAnswerResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.token)
	response, err := c.stub.GenerateAnswer(ctx, request)
	if err != nil {
		return nil, err
	}
	if response.RequestId != request.RequestId {
		return nil, errors.New("AI response request mismatch")
	}
	return response, nil
}
