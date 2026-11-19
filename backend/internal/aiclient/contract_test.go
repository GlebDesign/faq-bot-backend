package aiclient

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"faqbot/backend/internal/aipb"
	"google.golang.org/protobuf/proto"
)

func TestPythonGoWireFixture(t *testing.T) {
	data, err := os.ReadFile("../../../contracts/ai/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Question      string `json:"question"`
		RequestBase64 string `json:"request_base64"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	wire, err := base64.StdEncoding.DecodeString(fixture.RequestBase64)
	if err != nil {
		t.Fatal(err)
	}
	var request aipb.GenerateAnswerRequest
	if err = proto.Unmarshal(wire, &request); err != nil {
		t.Fatal(err)
	}
	if request.Question != fixture.Question || request.RequestId != "wire-request-1" || request.KnowledgeRevision == nil || *request.KnowledgeRevision != 7 {
		t.Fatal("Python wire fixture lost fields")
	}
	replay, err := (proto.MarshalOptions{Deterministic: true}).Marshal(&request)
	if err != nil {
		t.Fatal(err)
	}
	if base64.StdEncoding.EncodeToString(replay) != fixture.RequestBase64 {
		t.Fatal("Go wire differs from Python")
	}
}
