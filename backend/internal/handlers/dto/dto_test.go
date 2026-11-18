package dto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
)

const contractDir = "../../../../contracts"

func loadContract(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(filepath.Join(contractDir, "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return doc
}

func readFixtures[T any](t *testing.T, path string) T {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(contractDir, path))
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestOpenAPI(t *testing.T) { loadContract(t) }

func TestSchemaFixtures(t *testing.T) {
	doc := loadContract(t)
	cases := readFixtures[[]struct {
		Name   string
		Schema string
		Valid  bool
		Body   any
	}](t, "fixtures/schemas.json")
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			schema := doc.Components.Schemas[c.Schema]
			if schema == nil {
				t.Fatalf("unknown schema %s", c.Schema)
			}
			err := schema.Value.VisitJSON(c.Body)
			if (err == nil) != c.Valid {
				t.Fatalf("valid=%v, validation: %v", c.Valid, err)
			}
		})
	}
}

func TestResponseFixtures(t *testing.T) {
	doc := loadContract(t)
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	type responseCase struct {
		Name    string
		Method  string
		Path    string
		Status  int
		Headers map[string]string
		Body    json.RawMessage
	}
	cases := readFixtures[[]responseCase](t, "fixtures/responses.json")
	cases = append(cases, readFixtures[[]responseCase](t, "fixtures/flows.json")...)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			req, err := http.NewRequest(c.Method, "https://api.example.invalid"+c.Path, nil)
			if err != nil {
				t.Fatal(err)
			}
			route, params, err := router.FindRoute(req)
			if err != nil {
				t.Fatal(err)
			}
			headers := http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"req_demo"}, "Etag": {`"1"`}, "Retry-After": {"1"}}
			for name, value := range c.Headers {
				headers.Set(name, value)
			}
			body := []byte(c.Body)
			if c.Status != http.StatusNoContent && c.Body != nil && string(c.Body) != "null" && headers.Get("Cache-Control") != "no-store" {
				t.Fatal("sensitive JSON fixture lacks no-store")
			}
			if c.Status == http.StatusNoContent {
				body = nil
			}
			if c.Name == "downloadAttachment.success" {
				headers.Set("Content-Type", "application/octet-stream")
				headers.Set("Content-Disposition", `attachment; filename="diagnostic.txt"`)
				headers.Set("X-Content-Type-Options", "nosniff")
				headers.Set("Cache-Control", "no-store")
				body = []byte("synthetic attachment")
			}
			if c.Name == "previewAsset.success" || c.Name == "getWidgetAsset.success" {
				headers.Set("Content-Type", "image/png")
				headers.Set("X-Content-Type-Options", "nosniff")
				body = []byte("synthetic image body; MIME decoding is outside this test")
			}
			input := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
				Status:                 c.Status, Header: headers,
				Options: &openapi3filter.Options{IncludeResponseStatus: true},
			}
			input.SetBodyBytes(body)
			if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGoDTOsMatchSchemas(t *testing.T) {
	doc := loadContract(t)

	types := []reflect.Type{
		reflect.TypeFor[AuthCredentials](),
		reflect.TypeFor[AuthCSRF](),
		reflect.TypeFor[AuthIdentity](),
		reflect.TypeFor[AuthSession](),
		reflect.TypeFor[Package](),
		reflect.TypeFor[PackagePage](),
		reflect.TypeFor[Balance](),
		reflect.TypeFor[PackageRequestCreate](),
		reflect.TypeFor[PackageRequest](),
		reflect.TypeFor[PackageRequestPage](),
		reflect.TypeFor[PackageDecision](),
		reflect.TypeFor[AuditEvent](),
		reflect.TypeFor[AuditPage](),
		reflect.TypeFor[AgentCreate](),
		reflect.TypeFor[AgentUpdate](),
		reflect.TypeFor[Agent](),
		reflect.TypeFor[AgentPage](),
		reflect.TypeFor[PublishRequest](),
		reflect.TypeFor[EmbedCode](),
		reflect.TypeFor[PublicSource](),
		reflect.TypeFor[CabinetSource](),
		reflect.TypeFor[MessageCreate](),
		reflect.TypeFor[ChatAnswer](),
		reflect.TypeFor[ChatOperation](),
		reflect.TypeFor[TestChatAnswer](),
		reflect.TypeFor[Conversation](),
		reflect.TypeFor[ConversationPage](),
		reflect.TypeFor[Message](),
		reflect.TypeFor[MessagePage](),
		reflect.TypeFor[FeedbackCreate](),
		reflect.TypeFor[Feedback](),
		reflect.TypeFor[Analytics](),
		reflect.TypeFor[Unanswered](),
		reflect.TypeFor[UnansweredPage](),
		reflect.TypeFor[APIError](),
		reflect.TypeFor[Product](),
		reflect.TypeFor[FileUpload](),
		reflect.TypeFor[DocumentCreate](),
		reflect.TypeFor[DocumentUpdate](),
		reflect.TypeFor[Document](),
		reflect.TypeFor[DocumentPage](),
		reflect.TypeFor[Job](),
		reflect.TypeFor[CoverageCreate](),
		reflect.TypeFor[CoverageResult](),
		reflect.TypeFor[CoverageRun](),
		reflect.TypeFor[CoveragePage](),
		reflect.TypeFor[PartnerSessionCreate](),
		reflect.TypeFor[PartnerCredentialCreate](),
		reflect.TypeFor[PartnerCredential](),
		reflect.TypeFor[PartnerCredentialPage](),
		reflect.TypeFor[PartnerCredentialIssued](),
		reflect.TypeFor[SupportTicketCreate](),
		reflect.TypeFor[SupportStatusUpdate](),
		reflect.TypeFor[SupportStatusEvent](),
		reflect.TypeFor[SupportTicket](),
		reflect.TypeFor[SupportTicketPage](),
		reflect.TypeFor[AttachmentIntent](),
		reflect.TypeFor[Attachment](),
		reflect.TypeFor[ContactRequestCreate](),
		reflect.TypeFor[ContactReceipt](),
		reflect.TypeFor[ContactRequest](),
		reflect.TypeFor[ContactRequestPage](),
		reflect.TypeFor[ContactStatusUpdate](),
		reflect.TypeFor[WidgetConfig](),
		reflect.TypeFor[WidgetSettings](),
		reflect.TypeFor[Asset](),
		reflect.TypeFor[WidgetSessionCreate](),
		reflect.TypeFor[WidgetSession](),
	}
	if len(types) != len(doc.Components.Schemas) {
		t.Fatalf("DTOs: %d, schemas: %d", len(types), len(doc.Components.Schemas))
	}
	for _, typ := range types {
		t.Run(typ.Name(), func(t *testing.T) {
			schema := doc.Components.Schemas[typ.Name()].Value
			if typ.NumField() != len(schema.Properties) {
				t.Fatal("field count differs from OpenAPI")
			}
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				name := field.Tag.Get("json")
				property := schema.Properties[name]
				if property == nil {
					t.Fatalf("unknown JSON field %s", name)
				}
				if strings.Contains(name, ",") {
					t.Fatal("unexpected JSON option")
				}
				if (field.Type.Kind() == reflect.Pointer) != property.Value.Nullable {
					t.Fatalf("%s nullability differs", name)
				}
			}
			data, err := json.Marshal(schema.Example)
			if err != nil {
				t.Fatal(err)
			}
			value := reflect.New(typ).Interface()
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(value); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var roundtrip any
			if err := json.Unmarshal(encoded, &roundtrip); err != nil {
				t.Fatal(err)
			}
			var original any
			if err := json.Unmarshal(data, &original); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, roundtrip) {
				t.Fatal("Go DTO loses or changes example fields")
			}
		})
	}
}

func TestRequestFixtures(t *testing.T) {
	doc := loadContract(t)
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	cases := readFixtures[[]struct {
		Name    string
		Method  string
		Path    string
		Headers map[string]string
		Body    json.RawMessage
		Valid   bool
	}](t, "fixtures/requests.json")
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			req, err := http.NewRequest(c.Method, "https://api.example.invalid"+c.Path, bytes.NewReader(c.Body))
			if err != nil {
				t.Fatal(err)
			}
			for name, value := range c.Headers {
				req.Header.Set(name, value)
			}
			route, params, err := router.FindRoute(req)
			if err != nil {
				t.Fatal(err)
			}

			input := &openapi3filter.RequestValidationInput{
				Request: req, PathParams: params, Route: route,
				Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
			}
			err = validateFixtureRequest(input)
			if (err == nil) != c.Valid {
				t.Fatalf("valid=%v, validation: %v", c.Valid, err)
			}
		})
	}
}

func validateFixtureRequest(input *openapi3filter.RequestValidationInput) error {
	if reject, ok := input.Route.Operation.Extensions["x-reject-unknown-query-parameters"].(bool); ok && reject {
		allowed := map[string]bool{}
		for _, param := range input.Route.Operation.Parameters {
			if param.Value.In == "query" {
				allowed[param.Value.Name] = true
			}
		}
		for name := range input.Request.URL.Query() {
			if !allowed[name] {
				return errors.New("query parameter is outside session-scoped history contract")
			}
		}
	}
	return openapi3filter.ValidateRequest(context.Background(), input)
}
