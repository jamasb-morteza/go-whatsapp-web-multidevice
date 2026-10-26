package compat

import (
	"context"
	device "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/gofiber/fiber/v3"
	"testing"
)

type pairCodeDevice struct {
	*stubDeviceUsecase
	phone   string
	codeErr error
}

func (s *pairCodeDevice) LoginDeviceWithCode(_ context.Context, id, phone string) (string, error) {
	s.phone = phone
	return "ABCD-1234", s.codeErr
}
func TestSetSessionPhonePairingContract(t *testing.T) {
	_, dm, source, _, _ := newCompatTestApp()
	svc := &pairCodeDevice{stubDeviceUsecase: source}
	app := fiber.New()
	h := &Compat{dm: dm, deviceSvc: svc}
	app.Post("/set", h.SetSession)
	status, body := doRequest(app, "POST", "/set", "application/json", `{"session_name":"phone-test","pairing_method":"code","phone_number":"989123456789"}`)
	if status != 200 || body["success"] != true {
		t.Fatalf("code failed: %d %v", status, body)
	}
	data := body["data"].(map[string]any)
	if data["pair_code"] != "ABCD-1234" || data["status"] != "code_pending" || data["qr"] != nil {
		t.Fatalf("wrong code response: %v", data)
	}
	if svc.phone != "989123456789" || len(source.logins) != 0 || len(source.removed) != 0 || len(source.logouts) != 0 {
		t.Fatal("code flow invoked QR or unlink")
	}
}
func TestCodePairingPreservesSavedSession(t *testing.T) {
	_, dm, source, _, _ := newCompatTestApp()
	svc := &pairCodeDevice{stubDeviceUsecase: source, codeErr: pkgError.ErrSessionSaved}
	source.devices["phone-test"] = &device.Device{ID: "phone-test", State: device.DeviceStateDisconnected}
	app := fiber.New()
	h := &Compat{dm: dm, deviceSvc: svc}
	app.Post("/set", h.SetSession)
	status, body := doRequest(app, "POST", "/set", "application/json", `{"session_name":"phone-test","pairing_method":"code","phone_number":"989123456789"}`)
	if status != 202 || body["data"].(map[string]any)["status"] != "reconnecting" || len(source.removed) != 0 || len(source.logouts) != 0 {
		t.Fatalf("saved session replaced: %d %v", status, body)
	}
}
