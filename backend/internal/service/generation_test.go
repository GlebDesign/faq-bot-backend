package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"faqbot/backend/internal/models"
)

type generationQuotaFunc func(context.Context, string, string, time.Duration) (time.Time, error)

func (f generationQuotaFunc) Reserve(ctx context.Context, accountID, requestID string, timeout time.Duration) (time.Time, error) {
	return f(ctx, accountID, requestID, timeout)
}

func (f generationQuotaFunc) ReadGeneration(context.Context, string, string) (models.AIRequest, error) {
	return models.AIRequest{}, ErrInvalidGeneration
}

func (f generationQuotaFunc) Settle(context.Context, context.Context, string, string, models.GenerationOutcome) (models.AIRequest, error) {
	return models.AIRequest{}, ErrInvalidGeneration
}

func TestGenerationRejectsInvalidOrCanceledStart(t *testing.T) {
	const accountID = "00000000-0000-4000-8000-000000000001"
	const requestID = "00000000-0000-4000-8000-000000000002"
	store := generationQuotaFunc(func(context.Context, string, string, time.Duration) (time.Time, error) {
		t.Fatal("start reached quota store")
		return time.Time{}, nil
	})
	generation := NewGeneration(store)
	generate := func(context.Context) (*string, error) {
		t.Fatal("start reached adapter")
		return nil, nil
	}
	for _, tc := range []struct {
		accountID, requestID string
		timeout              time.Duration
		generate             func(context.Context) (*string, error)
	}{
		{accountID, requestID, time.Second, nil},
		{accountID, requestID, time.Nanosecond, generate},
		{accountID, "", time.Second, generate},
		{"invalid", requestID, time.Second, generate},
	} {
		if _, err := generation.Start(t.Context(), tc.accountID, tc.requestID, tc.timeout, tc.generate); !errors.Is(err, ErrInvalidGeneration) {
			t.Fatalf("invalid start returned %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := generation.Start(ctx, accountID, requestID, time.Second, generate); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start returned %v", err)
	}
}

func TestGenerationSkipsAdapterAfterCancelAtCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	deadline := time.Now().Add(time.Minute)
	store := generationQuotaFunc(func(context.Context, string, string, time.Duration) (time.Time, error) {
		cancel()
		return deadline, nil
	})
	generation := NewGeneration(store)
	result, err := generation.Start(ctx, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002", time.Minute, func(context.Context) (*string, error) {
		t.Fatal("adapter ran after cancel")
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) || !result.Deadline.Equal(deadline) {
		t.Fatalf("canceled start did not return committed deadline: %+v %v", result, err)
	}
}

func TestGenerationReportsCancellationDuringAdapter(t *testing.T) {
	adapterErr := errors.New("adapter failure")
	for _, callbackErr := range []error{nil, adapterErr} {
		ctx, cancel := context.WithCancel(t.Context())
		deadline := time.Now().Add(time.Hour)
		store := generationQuotaFunc(func(context.Context, string, string, time.Duration) (time.Time, error) {
			return deadline, nil
		})
		result, err := NewGeneration(store).Start(ctx, "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002", time.Minute, func(adapterCtx context.Context) (*string, error) {
			got, ok := adapterCtx.Deadline()
			if !ok || got.After(time.Now().Add(time.Minute)) {
				t.Error("adapter deadline exceeded its local timeout")
			}
			cancel()
			answer := "не применять"
			return &answer, callbackErr
		})
		cancel()
		if !errors.Is(err, context.Canceled) || callbackErr != nil && !errors.Is(err, callbackErr) || result.Answer != nil || !result.Deadline.Equal(deadline) {
			t.Fatalf("canceled adapter returned %+v, %v", result, err)
		}
	}
}
