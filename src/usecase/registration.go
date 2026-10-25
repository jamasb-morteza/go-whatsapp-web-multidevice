package usecase

import (
	"context"
	"fmt"
	"go.mau.fi/whatsmeow/types"
	"strings"
	"time"
)

type registrationLookup func(context.Context, []string) ([]types.IsOnWhatsAppResponse, error)

func checkRegistration(ctx context.Context, lookup registrationLookup, phone string) (bool, error) {
	phone = strings.TrimSuffix(strings.TrimSpace(phone), "@s.whatsapp.net")
	if phone == "" || strings.Contains(phone, "@") {
		return false, fmt.Errorf("invalid phone number")
	}
	if !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := lookup(ctx, []string{phone})
	if err != nil {
		return false, err
	}
	return len(result) > 0 && result[0].IsIn, nil
}
