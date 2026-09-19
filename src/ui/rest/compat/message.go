package compat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	domainSend "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/send"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// sendMessageBody mirrors the fields the legacy SendMessageController read.
// Location/Contact stay raw JSON text because the legacy API accepted them
// both as objects and as strings ("lat,lon" / JSON-encoded object), which is
// also why the body is parsed manually instead of via a single struct bind.
type sendMessageBody struct {
	SessionName string          `json:"session_name"`
	Mobile      string          `json:"mobile"`
	Message     string          `json:"message"`
	Text        string          `json:"text"`
	Location    json.RawMessage `json:"location"`
	Contact     json.RawMessage `json:"contact"`
	Mimetype    string          `json:"mimetype"`
	FileName    string          `json:"fileName"`
	Caption     string          `json:"caption"`
	JwtToken    string          `json:"jwt_token"`
}

// SendMessage mirrors POST /api/v1/social/message/send: one endpoint that
// dispatches on the fields present — text (message/text), a `file` multipart
// part (document), location, or contact — sending each as its own message in
// that order, exactly like the legacy controller. Unlike the legacy controller
// (which fire-and-forget the file/location/contact sends), every send is
// awaited, so those failures surface as HTTP 500 instead of silent success.
// callSend runs one usecase send call, converting the repo's panic-based
// error style (e.g. MustLogin panicking AuthError) into a returned error, so
// failures reach the caller as legacy-shaped 500s instead of the Recovery
// middleware's generic envelope.
func callSend(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("%v", r)
			}
		}
	}()
	return fn()
}

func (h *Compat) SendMessage(c fiber.Ctx) error {
	body, err := parseSendMessageBody(c)
	if err != nil {
		return sendLegacyError(c, http.StatusBadRequest, "invalid request body: "+err.Error(), "")
	}

	instance, err := h.resolveInstance(body.SessionName)
	if err != nil {
		// The legacy "not login" answer: no session means the caller must
		// create one first.
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"success":     false,
			"status":      "false",
			"status_code": fiber.StatusUnprocessableEntity,
			"message":     "you are not login",
		})
	}

	ctx := whatsapp.ContextWithDevice(c.Context(), instance)

	receiver := body.Mobile
	utils.SanitizePhone(&receiver)

	// Text: legacy precedence message || text.
	if text := firstNonEmpty(body.Message, body.Text); text != "" {
		request := domainSend.MessageRequest{
			BaseRequest: domainSend.BaseRequest{Phone: receiver},
			Message:     text,
		}
		if err := callSend(func() error {
			_, sendErr := h.sendSvc.SendText(ctx, request)
			return sendErr
		}); err != nil {
			return sendLegacyError(c, http.StatusInternalServerError, "message not sent", err.Error())
		}
	}

	// Document: the multipart part named "file"; fileName renames it like the
	// legacy controller did. The legacy payload always carried an empty
	// caption, so no caption is forwarded.
	if fileHeader, fileErr := c.FormFile("file"); fileErr == nil && fileHeader != nil {
		if body.FileName != "" {
			fileHeader.Filename = body.FileName
		}
		request := domainSend.FileRequest{
			BaseRequest: domainSend.BaseRequest{Phone: receiver},
			File:        fileHeader,
		}
		if err := callSend(func() error {
			_, sendErr := h.sendSvc.SendFile(ctx, request)
			return sendErr
		}); err != nil {
			return sendLegacyError(c, http.StatusInternalServerError, "message not sent", err.Error())
		}
	}

	if lat, lon, ok := parseLocation(string(body.Location)); ok {
		request := domainSend.LocationRequest{
			BaseRequest: domainSend.BaseRequest{Phone: receiver},
			Latitude:    lat,
			Longitude:   lon,
		}
		if err := callSend(func() error {
			_, sendErr := h.sendSvc.SendLocation(ctx, request)
			return sendErr
		}); err != nil {
			return sendLegacyError(c, http.StatusInternalServerError, "message not sent", err.Error())
		}
	}

	if contact, ok := parseContact(string(body.Contact)); ok {
		vcard, displayName, contactPhone, vcardErr := buildVCardForContact(contact)
		if vcardErr != nil {
			return sendLegacyError(c, http.StatusInternalServerError, "message not sent", vcardErr.Error())
		}
		request := domainSend.ContactRequest{
			BaseRequest:  domainSend.BaseRequest{Phone: receiver},
			ContactName:  displayName,
			ContactPhone: contactPhone,
			VCard:        vcard,
		}
		if err := callSend(func() error {
			_, sendErr := h.sendSvc.SendContact(ctx, request)
			return sendErr
		}); err != nil {
			return sendLegacyError(c, http.StatusInternalServerError, "message not sent", err.Error())
		}
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success":     true,
		"status":      "true",
		"status_code": fiber.StatusOK,
		"message":     "message sent successfully",
		"data":        struct{}{},
	})
}

// parseSendMessageBody accepts the three content types the legacy Express app
// parsed (JSON, urlencoded, multipart). Form values are re-quoted into JSON
// strings so Location/Contact flow through the same tolerant parsers.
func parseSendMessageBody(c fiber.Ctx) (sendMessageBody, error) {
	var body sendMessageBody

	contentType := strings.ToLower(c.Get(fiber.HeaderContentType))
	if strings.Contains(contentType, fiber.MIMEMultipartForm) || strings.Contains(contentType, "application/x-www-form-urlencoded") {
		body.SessionName = c.FormValue("session_name")
		body.Mobile = c.FormValue("mobile")
		body.Message = c.FormValue("message")
		body.Text = c.FormValue("text")
		body.Mimetype = c.FormValue("mimetype")
		body.FileName = c.FormValue("fileName")
		body.Caption = c.FormValue("caption")
		body.JwtToken = c.FormValue("jwt_token")
		if v := c.FormValue("location"); v != "" {
			body.Location = json.RawMessage(quoteJSON(v))
		}
		if v := c.FormValue("contact"); v != "" {
			body.Contact = json.RawMessage(quoteJSON(v))
		}
		return body, nil
	}

	raw := c.Body()
	if len(raw) == 0 {
		return body, nil
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return body, err
	}
	return body, nil
}

func quoteJSON(v string) string {
	quoted, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(quoted)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// sendLegacyError reproduces the legacy error bodies: the 422 shape has no
// data/exception fields, everything else carries exception_message plus an
// explicit JSON null data.
func sendLegacyError(c fiber.Ctx, statusCode int, message, exceptionMessage string) error {
	payload := fiber.Map{
		"success":     false,
		"status":      "false",
		"status_code": statusCode,
		"message":     message,
	}
	if statusCode != http.StatusUnprocessableEntity {
		payload["exception_message"] = exceptionMessage
		payload["data"] = nil
	}
	return c.Status(statusCode).JSON(payload)
}

// parseLocation accepts the legacy location field in both spellings: a
// "lat,lon" string (bare or JSON-quoted) or an object carrying
// latitude/longitude or the Baileys degreesLatitude/degreesLongitude names.
func parseLocation(raw string) (lat, lon string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return "", "", false
	}

	if strings.HasPrefix(raw, "{") {
		var obj struct {
			Latitude         *string `json:"latitude"`
			Longitude        *string `json:"longitude"`
			DegreesLatitude  *string `json:"degreesLatitude"`
			DegreesLongitude *string `json:"degreesLongitude"`
		}
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			return "", "", false
		}
		if obj.Latitude != nil || obj.DegreesLatitude != nil {
			return firstNonEmpty(deref(obj.Latitude), deref(obj.DegreesLatitude)),
				firstNonEmpty(deref(obj.Longitude), deref(obj.DegreesLongitude)), true
		}
		return "", "", false
	}

	if strings.HasPrefix(raw, `"`) {
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return "", "", false
		}
		return parseLocation(s)
	}

	parts := strings.Split(raw, ",")
	if len(parts) >= 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
	}
	return "", "", false
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// parseContact accepts the legacy contact field as an object, a JSON-encoded
// object string (the multipart form path), or null/empty (no contact). Parse
// failures are treated as "no contact" — the legacy controller's own broken
// JSON fell through to a near-empty vCard, which no caller relied on.
func parseContact(raw string) (map[string]any, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, false
	}

	if strings.HasPrefix(raw, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err != nil || obj == nil {
			return nil, false
		}
		return obj, true
	}

	if strings.HasPrefix(raw, `"`) {
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, false
		}
		return parseContact(s)
	}

	return nil, false
}
