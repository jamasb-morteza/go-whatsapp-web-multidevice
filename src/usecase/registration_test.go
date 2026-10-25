package usecase

import (
	"context"
	"errors"
	"go.mau.fi/whatsmeow/types"
	"testing"
)

func TestRegistrationErrorsAreNotNegativeResults(t *testing.T) {
	failure := errors.New("network timeout")
	ok, err := checkRegistration(context.Background(), func(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
		if phones[0] != "+123" {
			t.Fatal(phones)
		}
		return nil, failure
	}, "123@s.whatsapp.net")
	if ok || !errors.Is(err, failure) {
		t.Fatal(ok, err)
	}
}
func TestRegistrationUsesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := checkRegistration(ctx, func(ctx context.Context, _ []string) ([]types.IsOnWhatsAppResponse, error) { return nil, ctx.Err() }, "123")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestRegistrationPreservesConfirmedAbsence(t *testing.T) {
	ok, err := checkRegistration(context.Background(), func(context.Context, []string) ([]types.IsOnWhatsAppResponse, error) {
		return []types.IsOnWhatsAppResponse{{IsIn: false}}, nil
	}, "123")
	if ok || err != nil {
		t.Fatal(ok, err)
	}
}
