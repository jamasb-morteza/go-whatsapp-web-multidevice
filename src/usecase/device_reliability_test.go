package usecase

import (
	"context"
	"errors"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"testing"
)

func TestMissingDeviceHasTypedNotFoundError(t *testing.T) {
	service := &serviceDevice{manager: whatsapp.NewDeviceManager(nil, nil, nil)}
	_, err := service.GetDevice(context.Background(), "missing")
	if !errors.Is(err, pkgError.ErrDeviceNotFound) {
		t.Fatalf("expected typed not found: %v", err)
	}
}
