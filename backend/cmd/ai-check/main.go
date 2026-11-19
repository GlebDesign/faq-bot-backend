// Internal CLI: production HTTP chat/RBAC/quota are separate backend tasks.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"faqbot/backend/internal/aiclient"
	"faqbot/backend/internal/aipb"
)

func main() {
	account := flag.String("account", "", "Trusted account UUID")
	agent := flag.String("agent", "", "Trusted agent UUID")
	question := flag.String("question", "Сколько стоит доставка в Москву?", "Question")
	flag.Parse()
	client, err := aiclient.New(os.Getenv("AI_GRPC_ADDR"), os.Getenv("AI_SERVICE_TOKEN"), 180*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer client.Close()
	answer, err := client.Generate(context.Background(), &aipb.GenerateAnswerRequest{RequestId: "go-ai-check", TraceId: "local-check", AccountId: *account, AgentId: *agent, Question: *question})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(answer)
}
