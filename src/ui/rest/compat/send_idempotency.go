package compat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/idempotency"
	"github.com/gofiber/fiber/v3"
	"io"
	"os"
	"path/filepath"
)

func (h *Compat) sendIdempotency(c fiber.Ctx) error {
	key := c.Get("Idempotency-Key")
	if key == "" {
		return c.Next()
	} // legacy callers remain supported
	if len(key) > 200 {
		return sendLegacyError(c, 400, "invalid idempotency key", "")
	}
	body, err := parseSendMessageBody(c)
	if err != nil || body.SessionName == "" {
		return sendLegacyError(c, 400, "invalid session or request body", "")
	}
	body.JwtToken = "" // credentials do not define message identity
	canonical, _ := json.Marshal(body)
	fingerprint := sha256.New()
	fingerprint.Write(canonical)
	if file, err := c.FormFile("file"); err == nil && file != nil {
		fingerprint.Write([]byte(file.Filename))
		stream, err := file.Open()
		if err != nil {
			return sendLegacyError(c, 400, "cannot read file", "")
		}
		_, err = io.Copy(fingerprint, stream)
		stream.Close()
		if err != nil {
			return sendLegacyError(c, 400, "cannot read file", "")
		}
	}
	h.ledgerOnce.Do(func() {
		if h.sendLedger != nil {
			return
		}
		if err := os.MkdirAll(config.PathStorages, 0700); err != nil {
			h.ledgerErr = err
			return
		}
		h.sendLedger, h.ledgerErr = idempotency.Open("file:" + filepath.Join(config.PathStorages, "send-idempotency.db"))
	})
	if h.ledgerErr != nil {
		return sendLegacyError(c, 503, "delivery ledger unavailable", "")
	}
	// Hash the namespace, avoiding delimiter collisions or storing client secrets.
	operation := sha256.Sum256([]byte(body.SessionName + "\x00" + key))
	operationKey := hex.EncodeToString(operation[:])
	claim, err := h.sendLedger.Claim(operationKey, hex.EncodeToString(fingerprint.Sum(nil)))
	if err != nil {
		return sendLegacyError(c, 409, "idempotency claim rejected", "")
	}
	if !claim.Fresh {
		if claim.State == "complete" {
			c.Set("Content-Type", "application/json")
			return c.Status(claim.Status).Send(claim.Body)
		}
		return sendLegacyError(c, 409, "delivery in progress or outcome uncertain; do not resend with a new key", "")
	}
	if err = c.Next(); err != nil {
		return err
	} // leave an uncertain claim; never resend blindly
	if err = h.sendLedger.Complete(operationKey, c.Response().StatusCode(), c.Response().Body()); err != nil {
		return sendLegacyError(c, 503, "delivery outcome could not be recorded; check before retrying", "")
	}
	return nil
}
