package compat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/app"
	domainChat "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	domainSend "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/send"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/gofiber/fiber/v3"
)

// --- stubs -----------------------------------------------------------------

// stubDeviceUsecase delegates device creation to a real (nil-backed) device
// manager so ResolveDevice works, records lifecycle calls, and returns
// fabricated device snapshots for GetDevice.
type stubDeviceUsecase struct {
	device.IDeviceUsecase
	dm      *whatsapp.DeviceManager
	devices map[string]*device.Device

	added    []string
	logins   []string
	loginErr error
	logouts  []string
	removed  []string
}

func (s *stubDeviceUsecase) AddDevice(_ context.Context, deviceID string, _ *chatstorage.DeviceWebhookConfig) (*device.Device, error) {
	s.added = append(s.added, deviceID)
	if s.dm == nil {
		return &device.Device{ID: deviceID}, nil
	}
	instance, err := s.dm.CreateDevice(context.Background(), deviceID)
	if err != nil {
		return nil, err
	}
	return &device.Device{ID: instance.ID(), State: instance.State(), CreatedAt: instance.CreatedAt()}, nil
}

func (s *stubDeviceUsecase) GetDevice(_ context.Context, deviceID string) (*device.Device, error) {
	d, ok := s.devices[deviceID]
	if !ok {
		return nil, fmt.Errorf("device %s not found", deviceID)
	}
	snapshot := *d
	return &snapshot, nil
}

func (s *stubDeviceUsecase) LoginDevice(_ context.Context, deviceID string) (app.LoginResponse, error) {
	s.logins = append(s.logins, deviceID)
	return app.LoginResponse{Code: "2@AbCdEfGhIjKlMnOpQrStUvWxYz0123456789,AbCdEfGhIjKlMnOpQrStUvWxYz01234567,AbCdEfGh==", Duration: 20 * 1000000000}, s.loginErr
}

func (s *stubDeviceUsecase) LogoutDevice(_ context.Context, deviceID string) error {
	s.logouts = append(s.logouts, deviceID)
	return nil
}

func (s *stubDeviceUsecase) RemoveDevice(_ context.Context, deviceID string) error {
	s.removed = append(s.removed, deviceID)
	return nil
}

type stubSendUsecase struct {
	domainSend.ISendUsecase
	textReqs    []domainSend.MessageRequest
	fileReqs    []domainSend.FileRequest
	locReqs     []domainSend.LocationRequest
	contactReqs []domainSend.ContactRequest

	textErr    error
	fileErr    error
	locErr     error
	contactErr error
}

func (s *stubSendUsecase) SendText(_ context.Context, r domainSend.MessageRequest) (domainSend.GenericResponse, error) {
	s.textReqs = append(s.textReqs, r)
	if s.textErr != nil {
		return domainSend.GenericResponse{}, s.textErr
	}
	return domainSend.GenericResponse{MessageID: "MSG-" + r.Message, Status: "ok"}, nil
}

func (s *stubSendUsecase) SendFile(_ context.Context, r domainSend.FileRequest) (domainSend.GenericResponse, error) {
	s.fileReqs = append(s.fileReqs, r)
	if s.fileErr != nil {
		return domainSend.GenericResponse{}, s.fileErr
	}
	return domainSend.GenericResponse{Status: "ok"}, nil
}

func (s *stubSendUsecase) SendLocation(_ context.Context, r domainSend.LocationRequest) (domainSend.GenericResponse, error) {
	s.locReqs = append(s.locReqs, r)
	if s.locErr != nil {
		return domainSend.GenericResponse{}, s.locErr
	}
	return domainSend.GenericResponse{Status: "ok"}, nil
}

func (s *stubSendUsecase) SendContact(_ context.Context, r domainSend.ContactRequest) (domainSend.GenericResponse, error) {
	s.contactReqs = append(s.contactReqs, r)
	if s.contactErr != nil {
		return domainSend.GenericResponse{}, s.contactErr
	}
	return domainSend.GenericResponse{Status: "ok"}, nil
}

type stubChatUsecase struct {
	domainChat.IChatUsecase
	listReqs     []domainChat.ListChatsRequest
	listResponse domainChat.ListChatsResponse
	listErr      error

	msgReqs     []domainChat.GetChatMessagesRequest
	msgResponse domainChat.GetChatMessagesResponse
	msgErr      error
}

func (s *stubChatUsecase) ListChats(_ context.Context, r domainChat.ListChatsRequest) (domainChat.ListChatsResponse, error) {
	s.listReqs = append(s.listReqs, r)
	return s.listResponse, s.listErr
}

func (s *stubChatUsecase) GetChatMessages(_ context.Context, r domainChat.GetChatMessagesRequest) (domainChat.GetChatMessagesResponse, error) {
	s.msgReqs = append(s.msgReqs, r)
	return s.msgResponse, s.msgErr
}

// --- helpers ---------------------------------------------------------------

func newCompatTestApp() (*fiber.App, *whatsapp.DeviceManager, *stubDeviceUsecase, *stubSendUsecase, *stubChatUsecase) {
	dm := whatsapp.NewDeviceManager(nil, nil, nil)
	deviceSvc := &stubDeviceUsecase{dm: dm, devices: map[string]*device.Device{}}
	sendSvc := &stubSendUsecase{}
	chatSvc := &stubChatUsecase{}

	app := fiber.New()
	Init(app, dm, deviceSvc, sendSvc, chatSvc)
	return app, dm, deviceSvc, sendSvc, chatSvc
}

func doRequest(app *fiber.App, method, path, contentType, body string) (int, map[string]any) {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := app.Test(req)
	if err != nil {
		return 0, map[string]any{"__error__": err.Error()}
	}
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		decoded = map[string]any{"__error__": err.Error()}
	}
	return resp.StatusCode, decoded
}

func makeJWT(payload map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	encoded, _ := json.Marshal(payload)
	return fmt.Sprintf("%s.%s.", header, base64.RawURLEncoding.EncodeToString(encoded))
}

// --- session/set -----------------------------------------------------------

func TestSetSessionReturnsBase64QR(t *testing.T) {
	app, dm, deviceSvc, _, _ := newCompatTestApp()

	status, body := doRequest(app, http.MethodPost, "/api/v1/social/session/set", "application/json",
		`{"session_name":"sess1","jwt_token":"abc.def.ghi","app_url":"https://laravel.test"}`)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if body["success"] != true {
		t.Fatalf("success = %v, want true", body["success"])
	}
	if body["message"] != "QR code received, please scan the QR code." {
		t.Fatalf("message = %v", body["message"])
	}
	data, _ := body["data"].(map[string]any)
	if data == nil {
		t.Fatalf("data missing in response: %v", body)
	}
	qr, _ := data["qr"].(string)
	if !strings.HasPrefix(qr, "data:image/png;base64,") {
		t.Fatalf("qr = %q, want a data:image/png;base64, URL", qr)
	}
	png, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(qr, "data:image/png;base64,"))
	if err != nil {
		t.Fatalf("qr body is not base64: %v", err)
	}
	if !strings.HasPrefix(string(png), "\x89PNG") {
		t.Fatalf("qr payload is not a PNG")
	}

	if len(deviceSvc.added) != 1 || deviceSvc.added[0] != "sess1" {
		t.Fatalf("AddDevice calls = %v, want [sess1]", deviceSvc.added)
	}
	if len(deviceSvc.logins) != 1 || deviceSvc.logins[0] != "sess1" {
		t.Fatalf("LoginDevice calls = %v, want [sess1]", deviceSvc.logins)
	}
	if _, exists := dm.GetDevice("sess1"); !exists {
		t.Fatalf("device sess1 was not registered in the device manager")
	}
}

func TestSetSessionExistingSessionConflicts(t *testing.T) {
	app, dm, deviceSvc, _, _ := newCompatTestApp()
	if _, err := dm.CreateDevice(context.Background(), "sess1"); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	status, body := doRequest(app, http.MethodPost, "/api/v1/social/session/set", "application/json",
		`{"session_name":"sess1"}`)

	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	if body["success"] != false {
		t.Fatalf("success = %v, want false", body["success"])
	}
	if body["message"] != "Session already exists, please use another id." {
		t.Fatalf("message = %v", body["message"])
	}
	if len(deviceSvc.added) != 0 {
		t.Fatalf("AddDevice must not be called for an existing session, got %v", deviceSvc.added)
	}
}

func TestSetSessionMissingSessionNameRejected(t *testing.T) {
	app, _, _, _, _ := newCompatTestApp()

	status, body := doRequest(app, http.MethodPost, "/api/v1/social/session/set", "application/json", `{}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if body["success"] != false {
		t.Fatalf("success = %v, want false", body["success"])
	}
}

// --- session/status --------------------------------------------------------

func TestGetStatusUnknownSessionReportsDisconnected(t *testing.T) {
	app, _, deviceSvc, _, _ := newCompatTestApp()

	status, body := doRequest(app, http.MethodGet, "/api/v1/social/session/status?session_name=missing", "", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["success"] != false {
		t.Fatalf("success = %v, want false", body["success"])
	}
	data, _ := body["data"].(map[string]any)
	if data["status"] != "disconnected" {
		t.Fatalf("data.status = %v, want disconnected", data["status"])
	}
	if data["whatsapp_account_id"] != "" {
		t.Fatalf("data.whatsapp_account_id = %v, want empty", data["whatsapp_account_id"])
	}
	if len(deviceSvc.added) != 0 {
		t.Fatalf("no device lifecycle calls expected, got %v", deviceSvc.added)
	}
}

func TestGetStatusMapsLoggedInToAuthenticated(t *testing.T) {
	app, _, deviceSvc, _, _ := newCompatTestApp()
	deviceSvc.devices["sess1"] = &device.Device{
		State: device.DeviceStateLoggedIn,
		JID:   "628123456789:1@s.whatsapp.net",
	}

	jwtToken := makeJWT(map[string]any{"phone_number": "628123456789"})
	status, body := doRequest(app, http.MethodPost, "/api/v1/social/session/status", "application/json",
		fmt.Sprintf(`{"session_name":"sess1","jwt_token":%q}`, jwtToken))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["success"] != true {
		t.Fatalf("success = %v, want true", body["success"])
	}
	data, _ := body["data"].(map[string]any)
	if data["status"] != "authenticated" {
		t.Fatalf("data.status = %v, want authenticated", data["status"])
	}
	if data["whatsapp_account_id"] != "628123456789:1@s.whatsapp.net" {
		t.Fatalf("data.whatsapp_account_id = %v", data["whatsapp_account_id"])
	}
}

func TestGetStatusConnectedWithoutLogin(t *testing.T) {
	app, _, deviceSvc, _, _ := newCompatTestApp()
	deviceSvc.devices["sess1"] = &device.Device{State: device.DeviceStateConnected}

	status, body := doRequest(app, http.MethodGet, "/api/v1/social/session/status?session_name=sess1", "", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	data, _ := body["data"].(map[string]any)
	if data["status"] != "connected" {
		t.Fatalf("data.status = %v, want connected", data["status"])
	}
	if data["whatsapp_account_id"] != "" {
		t.Fatalf("data.whatsapp_account_id = %v, want empty", data["whatsapp_account_id"])
	}
}

func TestGetStatusPhoneMismatchPurgesSession(t *testing.T) {
	app, dm, deviceSvc, _, _ := newCompatTestApp()
	if _, err := dm.CreateDevice(context.Background(), "sess1"); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	deviceSvc.devices["sess1"] = &device.Device{
		State: device.DeviceStateLoggedIn,
		JID:   "628999888777:2@s.whatsapp.net",
	}

	jwtToken := makeJWT(map[string]any{"phone_number": "628123456789"})
	status, body := doRequest(app, http.MethodPost, "/api/v1/social/session/status", "application/json",
		fmt.Sprintf(`{"session_name":"sess1","jwt_token":%q}`, jwtToken))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["success"] != false {
		t.Fatalf("success = %v, want false", body["success"])
	}
	if body["message"] != "Phone number mismatch" {
		t.Fatalf("message = %v, want Phone number mismatch", body["message"])
	}
	data, _ := body["data"].(map[string]any)
	if data["status"] != "phone_mismatch" {
		t.Fatalf("data.status = %v, want phone_mismatch", data["status"])
	}
	if data["whatsapp_account_id"] != "628999888777:2@s.whatsapp.net" {
		t.Fatalf("data.whatsapp_account_id = %v", data["whatsapp_account_id"])
	}
	if len(deviceSvc.logouts) != 1 || deviceSvc.logouts[0] != "sess1" {
		t.Fatalf("LogoutDevice calls = %v, want [sess1]", deviceSvc.logouts)
	}
	if len(deviceSvc.removed) != 1 || deviceSvc.removed[0] != "sess1" {
		t.Fatalf("RemoveDevice calls = %v, want [sess1]", deviceSvc.removed)
	}
}

func TestGetStatusWithoutJWTNeverFlagsMismatch(t *testing.T) {
	app, _, deviceSvc, _, _ := newCompatTestApp()
	deviceSvc.devices["sess1"] = &device.Device{
		State: device.DeviceStateLoggedIn,
		JID:   "628999888777:2@s.whatsapp.net",
	}

	status, body := doRequest(app, http.MethodGet, "/api/v1/social/session/status?session_name=sess1", "", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["success"] != true {
		t.Fatalf("success = %v, want true", body["success"])
	}
	data, _ := body["data"].(map[string]any)
	if data["status"] != "authenticated" {
		t.Fatalf("data.status = %v, want authenticated", data["status"])
	}
	if len(deviceSvc.removed) != 0 {
		t.Fatalf("RemoveDevice must not be called without a jwt phone, got %v", deviceSvc.removed)
	}
}

// --- session/delete --------------------------------------------------------

func TestDeleteSessionNotFoundIsSuccess(t *testing.T) {
	app, _, _, _, _ := newCompatTestApp()

	status, body := doRequest(app, http.MethodPost, "/api/v1/social/session/delete", "application/json",
		`{"session_name":"ghost"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["success"] != true {
		t.Fatalf("success = %v, want true", body["success"])
	}
	if body["message"] != "Session not found or already deleted." {
		t.Fatalf("message = %v", body["message"])
	}
}

func TestDeleteSessionPurgesDevice(t *testing.T) {
	app, dm, deviceSvc, _, _ := newCompatTestApp()
	if _, err := dm.CreateDevice(context.Background(), "sess1"); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	status, body := doRequest(app, http.MethodDelete, "/api/v1/social/session/delete?session_name=sess1", "", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["success"] != true {
		t.Fatalf("success = %v, want true", body["success"])
	}
	if body["message"] != "The session has been successfully deleted." {
		t.Fatalf("message = %v", body["message"])
	}
	if len(deviceSvc.logouts) != 1 || deviceSvc.logouts[0] != "sess1" {
		t.Fatalf("LogoutDevice calls = %v, want [sess1]", deviceSvc.logouts)
	}
	if len(deviceSvc.removed) != 1 || deviceSvc.removed[0] != "sess1" {
		t.Fatalf("RemoveDevice calls = %v, want [sess1]", deviceSvc.removed)
	}
}

// --- message/send ----------------------------------------------------------

func TestSendMessageText(t *testing.T) {
	app, dm, _, sendSvc, _ := newCompatTestApp()
	if _, err := dm.CreateDevice(context.Background(), "sess1"); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	status, body := doRequest(app, http.MethodPost, "/api/v1/social/message/send", "application/json",
		`{"session_name":"sess1","mobile":"628123","message":"hello"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["success"] != true || body["status"] != "true" {
		t.Fatalf("body = %v, want success true status true", body)
	}
	if body["status_code"] != float64(200) {
		t.Fatalf("status_code = %v, want 200", body["status_code"])
	}
	if body["message"] != "message sent successfully" {
		t.Fatalf("message = %v", body["message"])
	}
	if data, ok := body["data"].(map[string]any); !ok || len(data) != 0 {
		t.Fatalf("data = %v, want an empty object", body["data"])
	}
	if len(sendSvc.textReqs) != 1 {
		t.Fatalf("SendText calls = %d, want 1", len(sendSvc.textReqs))
	}
	if sendSvc.textReqs[0].Phone != "628123@s.whatsapp.net" {
		t.Fatalf("Phone = %q, want 628123@s.whatsapp.net", sendSvc.textReqs[0].Phone)
	}
	if sendSvc.textReqs[0].Message != "hello" {
		t.Fatalf("Message = %q, want hello", sendSvc.textReqs[0].Message)
	}
}

func TestSendMessageUsesTextFieldFallback(t *testing.T) {
	app, dm, _, sendSvc, _ := newCompatTestApp()
	_, _ = dm.CreateDevice(context.Background(), "sess1")

	_, _ = doRequest(app, http.MethodPost, "/api/v1/social/message/send", "application/json",
		`{"session_name":"sess1","mobile":"628123","text":"via text field"}`)
	if len(sendSvc.textReqs) != 1 || sendSvc.textReqs[0].Message != "via text field" {
		t.Fatalf("textReqs = %+v, want one send with the text field value", sendSvc.textReqs)
	}
}

func TestSendMessageUnknownSessionIsNotLogin(t *testing.T) {
	app, _, _, _, _ := newCompatTestApp()

	status, body := doRequest(app, http.MethodPost, "/api/v1/social/message/send", "application/json",
		`{"session_name":"ghost","mobile":"628123","message":"hello"}`)
	if status != fiber.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", status)
	}
	if body["success"] != false || body["status"] != "false" || body["message"] != "you are not login" {
		t.Fatalf("body = %v", body)
	}
	if body["status_code"] != float64(fiber.StatusUnprocessableEntity) {
		t.Fatalf("status_code = %v, want 422", body["status_code"])
	}
	if _, hasData := body["data"]; hasData {
		t.Fatalf("422 response must not carry data, got %v", body["data"])
	}
	if _, hasException := body["exception_message"]; hasException {
		t.Fatalf("422 response must not carry exception_message")
	}
}

func TestSendMessageSendFailureSurfacesException(t *testing.T) {
	app, dm, _, sendSvc, _ := newCompatTestApp()
	_, _ = dm.CreateDevice(context.Background(), "sess1")
	sendSvc.textErr = fmt.Errorf("whatsapp closed")

	status, body := doRequest(app, http.MethodPost, "/api/v1/social/message/send", "application/json",
		`{"session_name":"sess1","mobile":"628123","message":"hello"}`)
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", status)
	}
	if body["success"] != false || body["status"] != "false" || body["message"] != "message not sent" {
		t.Fatalf("body = %v", body)
	}
	if body["exception_message"] != "whatsapp closed" {
		t.Fatalf("exception_message = %v, want whatsapp closed", body["exception_message"])
	}
	if data, ok := body["data"]; !ok || data != nil {
		t.Fatalf("data = %v, want JSON null present", body["data"])
	}
}

// panickingSendUsecase reproduces the repo's MustLogin behavior: a panic with
// a pkgError AuthError instead of a returned error. The legacy layer must
// convert it into the legacy 500 shape rather than letting the Recovery
// middleware answer with its own envelope.
type panickingSendUsecase struct {
	domainSend.ISendUsecase
}

func (s *panickingSendUsecase) SendText(context.Context, domainSend.MessageRequest) (domainSend.GenericResponse, error) {
	panic(pkgError.ErrNotLoggedIn)
}

func TestSendMessagePanicBecomesLegacy500(t *testing.T) {
	dm := whatsapp.NewDeviceManager(nil, nil, nil)
	deviceSvc := &stubDeviceUsecase{dm: dm, devices: map[string]*device.Device{}}
	sendSvc := &panickingSendUsecase{}
	chatSvc := &stubChatUsecase{}

	app := fiber.New()
	Init(app, dm, deviceSvc, sendSvc, chatSvc)
	_, _ = dm.CreateDevice(context.Background(), "sess1")

	status, body := doRequest(app, http.MethodPost, "/api/v1/social/message/send", "application/json",
		`{"session_name":"sess1","mobile":"628123","message":"hello"}`)
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (panics must not surface as 401)", status)
	}
	if body["message"] != "message not sent" {
		t.Fatalf("message = %v, want message not sent", body["message"])
	}
	if body["exception_message"] != "you are not logged in" {
		t.Fatalf("exception_message = %v, want you are not logged in", body["exception_message"])
	}
}

func TestSendMessageLocationString(t *testing.T) {
	app, dm, _, sendSvc, _ := newCompatTestApp()
	_, _ = dm.CreateDevice(context.Background(), "sess1")

	status, _ := doRequest(app, http.MethodPost, "/api/v1/social/message/send", "application/json",
		`{"session_name":"sess1","mobile":"628123","location":"35.7,51.4"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(sendSvc.locReqs) != 1 {
		t.Fatalf("SendLocation calls = %d, want 1", len(sendSvc.locReqs))
	}
	if sendSvc.locReqs[0].Latitude != "35.7" || sendSvc.locReqs[0].Longitude != "51.4" {
		t.Fatalf("location = %s,%s, want 35.7,51.4", sendSvc.locReqs[0].Latitude, sendSvc.locReqs[0].Longitude)
	}
}

func TestSendMessageLocationObject(t *testing.T) {
	app, dm, _, sendSvc, _ := newCompatTestApp()
	_, _ = dm.CreateDevice(context.Background(), "sess1")

	payload := `{"session_name":"sess1","mobile":"628123","location":{"degreesLatitude":"35.7","degreesLongitude":"51.4"}}`
	_, _ = doRequest(app, http.MethodPost, "/api/v1/social/message/send", "application/json", payload)
	if len(sendSvc.locReqs) != 1 || sendSvc.locReqs[0].Latitude != "35.7" || sendSvc.locReqs[0].Longitude != "51.4" {
		t.Fatalf("locReqs = %+v, want 35.7/51.4", sendSvc.locReqs)
	}
}

func TestSendMessageContactObjectBuildsVCard(t *testing.T) {
	app, dm, _, sendSvc, _ := newCompatTestApp()
	_, _ = dm.CreateDevice(context.Background(), "sess1")

	payload := `{"session_name":"sess1","mobile":"628123","contact":{
		"full_name":"Ali Reza","first_name":"Ali","last_name":"Reza",
		"main_phone_number":"09382208977","company_name":"ACME","city":"Tehran"}}`
	_, _ = doRequest(app, http.MethodPost, "/api/v1/social/message/send", "application/json", payload)

	if len(sendSvc.contactReqs) != 1 {
		t.Fatalf("SendContact calls = %d, want 1", len(sendSvc.contactReqs))
	}
	req := sendSvc.contactReqs[0]
	if req.ContactName != "Ali Reza" {
		t.Fatalf("ContactName = %q, want Ali Reza", req.ContactName)
	}
	if req.ContactPhone != "989382208977" {
		t.Fatalf("ContactPhone = %q, want 989382208977", req.ContactPhone)
	}
	for _, want := range []string{
		"BEGIN:VCARD", "VERSION:3.0", "N:Reza;Ali;;;", "FN:Ali Reza",
		"TEL;type=VOICE;CELL;waid=989382208977:+98 938 220 8977", "ORG:ACME", "ADR;type=HOME:;;;Tehran;;;",
	} {
		if !strings.Contains(req.VCard, want) {
			t.Fatalf("VCard = %q, missing %q", req.VCard, want)
		}
	}
	if !strings.HasSuffix(req.VCard, "END:VCARD\r\n") {
		t.Fatalf("VCard must end with CRLF-framed END:VCARD, got %q", req.VCard)
	}
}

func TestSendMessageMultipartFileWithRename(t *testing.T) {
	app, dm, _, sendSvc, _ := newCompatTestApp()
	_, _ = dm.CreateDevice(context.Background(), "sess1")

	var buf strings.Builder
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("session_name", "sess1"); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	if err := writer.WriteField("mobile", "628123"); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	if err := writer.WriteField("fileName", "renamed.pdf"); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	part, _ := writer.CreateFormFile("file", "orig.pdf")
	_, _ = part.Write([]byte("%PDF-1.4 fake"))
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/social/message/send", strings.NewReader(buf.String()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(sendSvc.fileReqs) != 1 {
		t.Fatalf("SendFile calls = %d, want 1", len(sendSvc.fileReqs))
	}
	if sendSvc.fileReqs[0].File == nil {
		t.Fatalf("File = nil, want populated header")
	}
	if sendSvc.fileReqs[0].File.Filename != "renamed.pdf" {
		t.Fatalf("File.Filename = %q, want renamed.pdf", sendSvc.fileReqs[0].File.Filename)
	}
}

// --- chat ------------------------------------------------------------------

func TestChatListUnknownSessionNotFound(t *testing.T) {
	app, _, _, _, _ := newCompatTestApp()

	status, body := doRequest(app, http.MethodGet, "/api/v1/social/chat/list?session_name=ghost", "", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if body["success"] != false || body["message"] != "Session not found." {
		t.Fatalf("body = %v", body)
	}
}

func TestChatListReturnsChatJIDs(t *testing.T) {
	app, dm, _, _, chatSvc := newCompatTestApp()
	_, _ = dm.CreateDevice(context.Background(), "sess1")
	chatSvc.listResponse = domainChat.ListChatsResponse{
		Data: []domainChat.ChatInfo{
			{JID: "628111@s.whatsapp.net"},
			{JID: "120363@g.us"},
		},
	}

	status, body := doRequest(app, http.MethodGet, "/api/v1/social/chat/list?session_name=sess1", "", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	data, _ := body["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("data = %v, want two jids", body["data"])
	}
	if data[0] != "628111@s.whatsapp.net" || data[1] != "120363@g.us" {
		t.Fatalf("data = %v", data)
	}
	if len(chatSvc.listReqs) != 1 || chatSvc.listReqs[0].Limit != chatListPageSize || chatSvc.listReqs[0].Offset != 0 {
		t.Fatalf("listReqs = %+v, want one page of %d from offset 0", chatSvc.listReqs, chatListPageSize)
	}
}

func TestChatGetUnknownSessionNotFound(t *testing.T) {
	app, _, _, _, _ := newCompatTestApp()

	status, body := doRequest(app, http.MethodGet, "/api/v1/social/chat/get?session_name=ghost&mobile=628123", "", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if body["message"] != "Session not found." {
		t.Fatalf("message = %v", body["message"])
	}
}

func TestChatGetMapsMessagesToLegacyShape(t *testing.T) {
	app, dm, _, _, chatSvc := newCompatTestApp()
	_, _ = dm.CreateDevice(context.Background(), "sess1")
	chatSvc.msgResponse = domainChat.GetChatMessagesResponse{
		Data: []domainChat.MessageInfo{
			{ID: "m1", ChatJID: "628123@s.whatsapp.net", Content: "hello", IsFromMe: true,
				Timestamp: "2026-09-19T10:00:00Z", MediaType: ""},
			{ID: "m2", ChatJID: "628123@s.whatsapp.net", Content: "see attachment", MediaType: "document",
				Filename: "report.pdf", URL: "https://example.com/report.pdf"},
		},
	}

	status, body := doRequest(app, http.MethodGet, "/api/v1/social/chat/get?session_name=sess1&mobile=628123&limit=10", "", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	data, _ := body["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("data = %v, want two messages", body["data"])
	}

	first, _ := data[0].(map[string]any)
	key, _ := first["key"].(map[string]any)
	if key["id"] != "m1" || key["remoteJid"] != "628123@s.whatsapp.net" || key["fromMe"] != true {
		t.Fatalf("key = %v, want m1/fromMe true", key)
	}
	msg, _ := first["message"].(map[string]any)
	if msg["conversation"] != "hello" {
		t.Fatalf("message = %v, want conversation hello", msg)
	}
	if first["messageTimestamp"] != float64(1789812000) {
		t.Fatalf("messageTimestamp = %v, want 1789812000", first["messageTimestamp"])
	}

	second, _ := data[1].(map[string]any)
	secondMsg, _ := second["message"].(map[string]any)
	doc, ok := secondMsg["documentMessage"].(map[string]any)
	if !ok || doc["fileName"] != "report.pdf" {
		t.Fatalf("documentMessage = %v, want fileName report.pdf", secondMsg)
	}

	if len(chatSvc.msgReqs) != 1 || chatSvc.msgReqs[0].ChatJID != "628123@s.whatsapp.net" || chatSvc.msgReqs[0].Limit != 10 {
		t.Fatalf("msgReqs = %+v", chatSvc.msgReqs)
	}
}

// --- jwt / helpers ---------------------------------------------------------

func TestPhoneFromJWT(t *testing.T) {
	token := makeJWT(map[string]any{"phone_number": "628123456789", "user_id": float64(42)})
	if got := phoneFromJWT(token); got != "628123456789" {
		t.Fatalf("phoneFromJWT = %q, want 628123456789", got)
	}
	// Garbage tokens degrade to "" instead of failing the request.
	if got := phoneFromJWT("not-a-jwt"); got != "" {
		t.Fatalf("phoneFromJWT(garbage) = %q, want empty", got)
	}
	if got := phoneFromJWT(""); got != "" {
		t.Fatalf("phoneFromJWT(empty) = %q, want empty", got)
	}
}

func TestFormatDisplayNumberRejectsNonIranianNumbers(t *testing.T) {
	if _, err := formatDisplayNumber("+14155550123"); err == nil {
		t.Fatalf("expected an error for a non-Iranian number, matching the legacy behavior")
	}
	if got, err := formatDisplayNumber("09382208977"); err != nil || got != "+98 938 220 8977" {
		t.Fatalf("formatDisplayNumber = %q err %v, want +98 938 220 8977", got, err)
	}
}
