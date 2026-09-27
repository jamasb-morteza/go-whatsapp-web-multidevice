package compat

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/gofiber/fiber/v3"
	"github.com/sirupsen/logrus"
	"github.com/skip2/go-qrcode"
)

// SetSession mirrors POST /api/v1/social/session/set: create the session and
// answer with an inline base64 QR data URL. The legacy app_url field is stored
// as the device webhook URL so events reach the caller. An existing
// logged-in device answers "Session already connected."; an existing unlinked
// device gets a fresh QR instead of the legacy 409, which is now reserved for
// a stored pairing that cannot produce one (ErrSessionSaved). isLegacy has no
// meaning for a multi-device-only implementation.
func (h *Compat) SetSession(c fiber.Ctx) error {
	var req struct {
		SessionName string `json:"session_name" form:"session_name"`
		JwtToken    string `json:"jwt_token" form:"jwt_token"`
		AppURL      string `json:"app_url" form:"app_url"`
		IsLegacy    string `json:"isLegacy" form:"isLegacy"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return nodeResponse(c, fiber.StatusBadRequest, false, "Invalid request body", nil)
	}

	sessionName := strings.TrimSpace(req.SessionName)
	if sessionName == "" {
		return nodeResponse(c, fiber.StatusBadRequest, false, "session_name is required", nil)
	}
	appURL := strings.TrimSpace(req.AppURL)

	alreadyConnected := func() error {
		return nodeResponse(c, fiber.StatusOK, true, "Session already connected.", fiber.Map{
			"qr":        "",
			"device_id": sessionName,
			"status":    "connected",
		})
	}

	exists := false
	if h.dm != nil {
		_, exists = h.dm.GetDevice(sessionName)
	}
	if exists {
		// Refresh the webhook even for a connected device so a caller can
		// re-point it, then answer without touching the WhatsApp session.
		if err := h.setDeviceWebhookFromAppURL(c.Context(), sessionName, appURL); err != nil {
			logrus.Errorf("[COMPAT] webhook update for %s failed: %v", sessionName, err)
			return nodeResponse(c, fiber.StatusInternalServerError, false, "Unable to create session.", nil)
		}
		if dev, err := h.deviceSvc.GetDevice(c.Context(), sessionName); err == nil && dev != nil && dev.State == device.DeviceStateLoggedIn {
			return alreadyConnected()
		}
	} else {
		var webhook *chatstorage.DeviceWebhookConfig
		if appURL != "" {
			webhook = &chatstorage.DeviceWebhookConfig{WebhookURL: &appURL}
		}
		if _, err := h.deviceSvc.AddDevice(c.Context(), sessionName, webhook); err != nil {
			return nodeResponse(c, fiber.StatusInternalServerError, false, "Unable to create session.", nil)
		}
	}

	login, err := h.deviceSvc.LoginDevice(c.Context(), sessionName)
	if err != nil {
		// A device with a stored pairing cannot produce a new QR code; the
		// legacy service surfaced that case as "session already exists".
		if errors.Is(err, pkgError.ErrAlreadyLoggedIn) {
			return alreadyConnected()
		}
		if errors.Is(err, pkgError.ErrSessionSaved) {
			// Login's ErrQRStoreContainsID path checks IsLoggedIn while the
			// reconnect handshake may still be in flight, so a just-connected
			// device can land here; trust a fresh state lookup over that race.
			if dev, devErr := h.deviceSvc.GetDevice(c.Context(), sessionName); devErr == nil && dev != nil &&
				(dev.State == device.DeviceStateLoggedIn || dev.JID != "") {
				return alreadyConnected()
			}
			return nodeResponse(c, fiber.StatusConflict, false, "Session already exists, please use another id.", nil)
		}
		return nodeResponse(c, fiber.StatusInternalServerError, false, "Unable to create session.", nil)
	}

	png, err := qrcode.Encode(login.Code, qrcode.Medium, 512)
	if err != nil {
		return nodeResponse(c, fiber.StatusInternalServerError, false, "Unable to create QR code.", nil)
	}
	qrData := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)

	return nodeResponse(c, fiber.StatusOK, true, "QR code received, please scan the QR code.", fiber.Map{
		"qr":        qrData,
		"device_id": sessionName,
	})
}

// setDeviceWebhookFromAppURL stores the caller's app_url as the device webhook
// URL, preserving any saved secret/event filter. An unchanged URL is a no-op
// so QR polling doesn't rewrite (and broadcast) on every attempt.
func (h *Compat) setDeviceWebhookFromAppURL(ctx context.Context, sessionName, appURL string) error {
	if appURL == "" {
		return nil
	}
	cfg, err := h.deviceSvc.GetDeviceWebhookConfig(ctx, sessionName)
	if err != nil && !errors.Is(err, pkgError.ErrDeviceNotFound) {
		return err
	}
	if cfg == nil {
		cfg = &chatstorage.DeviceWebhookConfig{}
	}
	if cfg.WebhookURL != nil && *cfg.WebhookURL == appURL {
		return nil
	}
	cfg.WebhookURL = &appURL
	return h.deviceSvc.SetDeviceWebhookConfig(ctx, sessionName, cfg)
}

// GetStatus mirrors GET|POST /api/v1/social/session/status. Unknown or missing
// sessions answer HTTP 200 with status "disconnected" rather than an error,
// matching the legacy contract the pollers depend on. The jwt_token's
// phone_number is matched against the logged-in account: on mismatch the
// device is logged out and purged, and status "phone_mismatch" is returned.
func (h *Compat) GetStatus(c fiber.Ctx) error {
	sessionName, jwtToken := sessionParams(c)
	allowedPhone := phoneFromJWT(jwtToken)

	disconnected := func() error {
		return nodeResponse(c, fiber.StatusOK, false, "", fiber.Map{
			"status": "disconnected", "whatsapp_account_id": "",
			"device_id": sessionName,
		})
	}

	if sessionName == "" {
		return disconnected()
	}

	dev, err := h.deviceSvc.GetDevice(c.Context(), sessionName)
	if err != nil || dev == nil {
		return disconnected()
	}

	whatsappAccountID := dev.JID
	if whatsappAccountID != "" && allowedPhone != "" && !strings.Contains(whatsappAccountID, allowedPhone) {
		logrus.Warnf("[COMPAT] phone mismatch for session %s: allowed %s, actual %s",
			sessionName, allowedPhone, whatsappAccountID)
		h.purgeSession(c, sessionName)
		return nodeResponse(c, fiber.StatusOK, false, "Phone number mismatch", fiber.Map{
			"status": "phone_mismatch", "whatsapp_account_id": whatsappAccountID,
			"device_id": sessionName,
		})
	}

	return nodeResponse(c, fiber.StatusOK, true, "", fiber.Map{
		"status":              legacyStatusFromDeviceState(dev.State),
		"whatsapp_account_id": whatsappAccountID,
		"device_id":           sessionName,
	})
}

// legacyStatusFromDeviceState maps this service's device states onto the
// legacy status vocabulary. "logged_in" is what the legacy service called
// "authenticated" (connected with a paired account); there is no equivalent
// for its transient "disconnecting" state.
func legacyStatusFromDeviceState(state device.DeviceState) string {
	switch state {
	case device.DeviceStateLoggedIn:
		return "authenticated"
	case device.DeviceStateConnected:
		return "connected"
	case device.DeviceStateConnecting:
		return "connecting"
	default:
		return "disconnected"
	}
}

// DeleteSession mirrors POST|DELETE /api/v1/social/session/delete. It always
// answers success like the legacy handler, even when logout of an already-dead
// session fails, because the local cleanup still removes the session.
func (h *Compat) DeleteSession(c fiber.Ctx) error {
	sessionName, _ := sessionParams(c)

	if sessionName == "" {
		return nodeResponse(c, fiber.StatusOK, true, "Session not found or already deleted.", nil)
	}
	if h.dm != nil {
		if _, exists := h.dm.GetDevice(sessionName); !exists {
			return nodeResponse(c, fiber.StatusOK, true, "Session not found or already deleted.", nil)
		}
	}

	h.purgeSession(c, sessionName)
	return nodeResponse(c, fiber.StatusOK, true, "The session has been successfully deleted.", nil)
}

// purgeSession removes a device the way the legacy service's mismatch pruning
// did: best-effort WhatsApp logout, then full local deletion. PurgeDevice
// already performs the unlink, so failures are logged, not propagated — the
// legacy endpoints answered success regardless.
func (h *Compat) purgeSession(c fiber.Ctx, sessionName string) {
	if err := h.deviceSvc.LogoutDevice(c.Context(), sessionName); err != nil {
		logrus.Debugf("[COMPAT] logout during purge of %s failed (continuing): %v", sessionName, err)
	}
	if err := h.deviceSvc.RemoveDevice(c.Context(), sessionName); err != nil {
		logrus.Warnf("[COMPAT] purge of session %s failed: %v", sessionName, err)
	}
}
