// Package compat serves the REST contract of the legacy Node.js whatsapp-api
// service (Express + Baileys) so callers built against it keep working
// unchanged: identical paths, request fields, and response envelopes. A legacy
// session_name maps directly onto a device_id in this service.
//
// Deliberate deviations from the legacy service, all documented on the
// handlers: incoming-message webhooks are not re-emitted (the legacy global
// WEBHOOK_URL flow has no per-device equivalent here — configure the native
// device webhook instead), and file/location/contact send failures are
// reported as HTTP 500 instead of being fire-and-forget.
package compat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/send"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/gofiber/fiber/v3"
)

// Compat holds the services backing the legacy endpoints. The device manager
// is used directly (not via middleware) because the legacy API identifies the
// session in the request body/query, not the X-Device-Id header.
type Compat struct {
	dm        *whatsapp.DeviceManager
	deviceSvc device.IDeviceUsecase
	sendSvc   send.ISendUsecase
	chatSvc   chat.IChatUsecase
}

// Init registers the legacy routes at their original root paths. Register it
// after the Basic Auth middleware so the legacy surface is protected whenever
// Basic Auth is configured, and open otherwise — matching the legacy service,
// which had no authentication of its own.
func Init(app fiber.Router, dm *whatsapp.DeviceManager, deviceSvc device.IDeviceUsecase, sendSvc send.ISendUsecase, chatSvc chat.IChatUsecase) *Compat {
	h := &Compat{dm: dm, deviceSvc: deviceSvc, sendSvc: sendSvc, chatSvc: chatSvc}

	session := app.Group("/api/v1/social/session")
	session.Post("/set", h.SetSession)
	session.Get("/status", h.GetStatus)
	session.Post("/status", h.GetStatus)
	session.Post("/delete", h.DeleteSession)
	session.Delete("/delete", h.DeleteSession)

	app.Post("/api/v1/social/message/send", h.SendMessage)
	app.Get("/api/v1/social/chat/list", h.ChatList)
	app.Get("/api/v1/social/chat/get", h.ChatGet)

	return h
}

// nodeResponseBody is the legacy `response(res, ...)` envelope. Data is always
// present, defaulting to an empty object like the legacy helper.
type nodeResponseBody struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

func nodeResponse(c fiber.Ctx, status int, success bool, message string, data any) error {
	if data == nil {
		data = struct{}{}
	}
	return c.Status(status).JSON(nodeResponseBody{Success: success, Message: message, Data: data})
}

// resolveInstance maps a legacy session_name to a device. An empty name is an
// error: unlike DeviceMiddleware there is no single-device fallback, because
// the legacy endpoints always identify the session explicitly.
func (h *Compat) resolveInstance(sessionName string) (*whatsapp.DeviceInstance, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return nil, fmt.Errorf("session_name is required")
	}
	if h.dm == nil {
		return nil, fmt.Errorf("device manager not initialized")
	}
	instance, _, err := h.dm.ResolveDevice(sessionName)
	if err != nil {
		return nil, err
	}
	return instance, nil
}

// sessionParams reads session_name/jwt_token the way the legacy controllers
// did: the JSON body when it carries a session_name (POST), otherwise the
// query string (GET, or POST with query params).
func sessionParams(c fiber.Ctx) (sessionName, jwtToken string) {
	if c.Method() == fiber.MethodPost {
		var body struct {
			SessionName string `json:"session_name" form:"session_name"`
			JwtToken    string `json:"jwt_token" form:"jwt_token"`
		}
		if err := c.Bind().Body(&body); err == nil && strings.TrimSpace(body.SessionName) != "" {
			return strings.TrimSpace(body.SessionName), body.JwtToken
		}
	}
	return strings.TrimSpace(c.Query("session_name")), c.Query("jwt_token")
}

// decodeJWTPayload mirrors the legacy service's unverified decode: split on
// ".", base64-decode the payload segment, and parse it as JSON. It returns nil
// for anything malformed, exactly like the Node decoder returned null.
func decodeJWTPayload(token string) map[string]any {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	// Node's Buffer.from(x, 'base64') tolerates both alphabets and missing
	// padding; normalize to raw URL encoding before decoding.
	normalized := strings.NewReplacer("+", "-", "/", "_", "=", "").Replace(parts[1])
	decoded, err := base64.RawURLEncoding.DecodeString(normalized)
	if err != nil {
		return nil
	}
	var payload map[string]any
	if json.Unmarshal(decoded, &payload) != nil {
		return nil
	}
	return payload
}

// jwtString extracts a claim with the loose coercion of the legacy service:
// numbers render without their JSON float notation, anything unexpected is "".
func jwtString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	v, ok := payload[key]
	if !ok || v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%v", val)
	}
	return ""
}

func phoneFromJWT(token string) string {
	return jwtString(decodeJWTPayload(token), "phone_number")
}
