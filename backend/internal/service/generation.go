package service

import (
	"context"
	"errors"
	"time"

	"faqbot/backend/internal/models"
)

var (
	ErrInvalidGeneration      = errors.New("invalid generation request")
	ErrQuotaRequestNotFound   = errors.New("request not found for quota reservation")
	ErrQuotaRequestNotPending = errors.New("request no longer accepts generation")
	ErrQuotaAlreadyReserved   = errors.New("request already has a quota reservation")
	ErrQuotaExhausted         = errors.New("available quota exhausted")
	ErrQuotaReservationAbsent = errors.New("quota reservation not found")
	ErrQuotaInconsistent      = errors.New("quota reservation and request states are inconsistent")
)

type generationStore interface {
	Reserve(context.Context, string, string, time.Duration) (time.Time, error)
	ReadGeneration(context.Context, string, string) (models.AIRequest, error)
	Settle(context.Context, context.Context, string, string, models.GenerationOutcome) (models.AIRequest, error)
}

type Generation struct{ store generationStore }

func NewGeneration(store generationStore) *Generation { return &Generation{store: store} }

type GenerationResult struct {
	Deadline time.Time
	Answer   *string
}

func (g *Generation) Start(ctx context.Context, accountID, requestID string, timeout time.Duration, generate func(context.Context) (*string, error)) (GenerationResult, error) {
	if generate == nil {
		return GenerationResult{}, ErrInvalidGeneration
	}
	var answer *string
	deadline, err := g.start(ctx, accountID, requestID, timeout, func(ctx context.Context) error {
		var err error
		answer, err = generate(ctx)
		return err
	})
	if err != nil {
		return GenerationResult{Deadline: deadline}, err
	}
	return GenerationResult{Deadline: deadline, Answer: answer}, nil
}

func (g *Generation) Execute(ctx context.Context, accountID, requestID string, timeout time.Duration, generate func(context.Context) (*models.GeneratedAnswer, error)) (models.AIRequest, error) {
	if generate == nil {
		return models.AIRequest{}, ErrInvalidGeneration
	}
	var answer *models.GeneratedAnswer
	deadline, callErr := g.start(ctx, accountID, requestID, timeout, func(ctx context.Context) error {
		var err error
		answer, err = generate(ctx)
		return err
	})
	if errors.Is(callErr, ErrQuotaAlreadyReserved) && deadline.IsZero() {
		return g.store.ReadGeneration(ctx, accountID, requestID)
	}
	if deadline.IsZero() {
		return models.AIRequest{}, callErr
	}
	outcome := models.GenerationOutcome{}
	switch {
	case errors.Is(callErr, context.DeadlineExceeded):
		outcome.Failure = models.GenerationTimedOut
	case errors.Is(callErr, context.Canceled):
		outcome.Failure = models.GenerationCancelled
	case callErr != nil:
		outcome.Failure = models.GenerationFailed
	default:
		outcome.Answer = answer
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	result, err := g.store.Settle(settleCtx, ctx, accountID, requestID, outcome)
	if err != nil {
		return models.AIRequest{}, errors.Join(err, callErr)
	}
	return result, callErr
}

func (g *Generation) start(ctx context.Context, accountID, requestID string, timeout time.Duration, generate func(context.Context) error) (time.Time, error) {
	if !auditUUID.MatchString(accountID) || !auditUUID.MatchString(requestID) || timeout < time.Microsecond {
		return time.Time{}, ErrInvalidGeneration
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	deadline, err := g.store.Reserve(ctx, accountID, requestID, timeout)
	if err != nil {
		return time.Time{}, err
	}
	adapterDeadline := deadline
	if limit := time.Now().Add(timeout); limit.Before(deadline) {
		adapterDeadline = limit
	}
	adapterCtx, cancel := context.WithDeadline(ctx, adapterDeadline)
	defer cancel()
	if err := adapterCtx.Err(); err != nil {
		return deadline, err
	}
	err = generate(adapterCtx)
	if ctxErr := adapterCtx.Err(); ctxErr != nil {
		return deadline, errors.Join(err, ctxErr)
	}
	return deadline, err
}
