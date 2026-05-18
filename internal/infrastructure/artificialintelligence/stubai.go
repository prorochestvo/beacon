package artificialintelligence

import (
	"context"
	"fmt"
)

// stubAIDefaultResponse is a hard-coded JSON string conforming to
// RateExtractionRuleSchema(). It is used as the canned response for the stub
// client so the service can run end-to-end without a real AI API key. The
// schema_test.go file asserts that this value stays aligned with the schema.
const stubAIDefaultResponse = `{"rules":[{"method":"regex","pattern":"USD / KZT[\\s\\S]{1,500}?<div[^>]*>(\\d+\\.\\d+)</div>"}]}`

func newStubAIClient(completeResponse string) (AIClient, error) {
	return &stubClient{completeResponse: completeResponse}, nil
}

type stubClient struct {
	completeResponse string
}

func (s *stubClient) Name() string {
	return "StubAI"
}

func (s *stubClient) Model() string {
	return "stub"
}

func (s *stubClient) CheckUP(_ context.Context) error {
	if s.completeResponse == "" {
		return fmt.Errorf("stubai: dummy response is empty")
	}
	return nil
}

func (s *stubClient) Complete(_ context.Context, _, _ string) (string, error) {
	return s.completeResponse, nil
}
