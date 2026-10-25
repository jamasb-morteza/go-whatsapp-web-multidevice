package compat

import (
	"context"
	"errors"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/idempotency"
	"github.com/gofiber/fiber/v3"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyedSendReplaysWithoutSendingTwice(t *testing.T) {
	_, dm, devices, sender, chats := newCompatTestApp()
	if _, err := dm.CreateDevice(context.Background(), "session"); err != nil {
		t.Fatal(err)
	}
	ledger, err := idempotency.Open("file:" + filepath.Join(t.TempDir(), "sends.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	h := &Compat{dm: dm, deviceSvc: devices, sendSvc: sender, chatSvc: chats, sendLedger: ledger}
	app := fiber.New()
	app.Post("/send", h.sendIdempotency, h.SendMessage)
	request := func(key, text string) (int, string) {
		req := httptest.NewRequest("POST", "/send", strings.NewReader(`{"session_name":"session","mobile":"123","message":"`+text+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		response, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(body)
	}
	first, body := request("one", "hello")
	second, replayed := request("one", "hello")
	if first != 200 || second != 200 || body != replayed || len(sender.textReqs) != 1 {
		t.Fatal("duplicate send", first, second, len(sender.textReqs))
	}
	conflict, _ := request("one", "different")
	if conflict != 409 || len(sender.textReqs) != 1 {
		t.Fatal("conflicting content was sent")
	}
	sender.textErr = errors.New("ambiguous timeout")
	status, _ := request("two", "later")
	again, _ := request("two", "later")
	if status != 500 || again != 500 || len(sender.textReqs) != 2 {
		t.Fatal("failed delivery was resent", status, again, len(sender.textReqs))
	}
}
