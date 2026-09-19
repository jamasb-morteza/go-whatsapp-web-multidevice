package compat

import (
	"encoding/base64"
	"errors"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/gofiber/fiber/v3"
	"github.com/sirupsen/logrus"
	"github.com/skip2/go-qrcode"
)

// SetSession mirrors POST /api/v1/social/session/set: create the session and
// answer with an inline base64 QR data URL. The legacy service also accepted
// app_url/isLegacy; app_url is not mapped to a device webhook because this
// service's native webhook payload has a different shape than the legacy one,
// and isLegacy has no meaning for a multi-device-only implementation.
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

	if h.dm != nil {
		if _, exists := h.dm.GetDevice(sessionName); exists {
			return nodeResponse(c, fiber.StatusConflict, false, "Session already exists, please use another id.", nil)
		}
	}

	if _, err := h.deviceSvc.AddDevice(c.Context(), sessionName, nil); err != nil {
		return nodeResponse(c, fiber.StatusInternalServerError, false, "Unable to create session.", nil)
	}

	login, err := h.deviceSvc.LoginDevice(c.Context(), sessionName)
	if err != nil {
		// A device with a stored pairing cannot produce a new QR code; the
		// legacy service surfaced that case as "session already exists".
		if errors.Is(err, pkgError.ErrAlreadyLoggedIn) || errors.Is(err, pkgError.ErrSessionSaved) {
			return nodeResponse(c, fiber.StatusConflict, false, "Session already exists, please use another id.", nil)
		}
		return nodeResponse(c, fiber.StatusInternalServerError, false, "Unable to create session.", nil)
	}

	png, err := qrcode.Encode(login.Code, qrcode.Medium, 512)
	if err != nil {
		return nodeResponse(c, fiber.StatusInternalServerError, false, "Unable to create QR code.", nil)
	}
	qrData := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)

	return nodeResponse(c, fiber.StatusOK, true, "QR code received, please scan the QR code.", fiber.Map{"qr": qrData})
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
		})
	}

	return nodeResponse(c, fiber.StatusOK, true, "", fiber.Map{
		"status":              legacyStatusFromDeviceState(dev.State),
		"whatsapp_account_id": whatsappAccountID,
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
