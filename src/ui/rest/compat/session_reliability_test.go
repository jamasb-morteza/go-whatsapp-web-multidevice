package compat

import (
	"context"
	"errors"
	device "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/gofiber/fiber/v3"
	"testing"
)

type auditCleanupFailure struct{ device.IDeviceUsecase }

func (*auditCleanupFailure) LogoutDevice(context.Context, string) error { return nil }
func (*auditCleanupFailure) RemoveDevice(context.Context, string) error {
	return errors.New("simulated sqlite cleanup failure")
}

func TestAuditDisconnectDoesNotHideCleanupFailure(t *testing.T) {
	app := fiber.New()
	h := &Compat{deviceSvc: &auditCleanupFailure{}}
	app.Post("/delete", h.DeleteSession)
	status, body := doRequest(app, "POST", "/delete", "application/json", `{"session_name":"audit-only"}`)
	if status < 400 || body["success"] == true {
		t.Fatalf("cleanup failed but disconnect returned HTTP %d success=%v", status, body["success"])
	}
}

func TestAuditSavedDisconnectedPairingIsNotReportedConnected(t *testing.T) {
	app, dm, source, _, _ := newCompatTestApp()
	if _, err := dm.CreateDevice(context.Background(), "audit-only"); err != nil {
		t.Fatal(err)
	}
	source.devices["audit-only"] = &device.Device{ID: "audit-only", State: device.DeviceStateDisconnected, JID: "123@s.whatsapp.net"}
	source.loginErr = pkgError.ErrSessionSaved
	status, body := doRequest(app, "POST", "/api/v1/social/session/set", "application/json", `{"session_name":"audit-only"}`)
	if status == 200 && body["success"] == true {
		t.Fatalf("disconnected saved pairing reported connected: %v", body)
	}
}
