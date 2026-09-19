package compat

import (
	"strconv"
	"strings"
	"time"

	domainChat "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// chatListPageSize is the storage-side page size used to enumerate every chat
// for ChatList. The legacy endpoint returned the full in-memory chat list with
// no pagination, so pages are gathered until exhaustion.
const chatListPageSize = 100

// ChatList mirrors GET /api/v1/social/chat/list: the JIDs of every chat for
// the session. The legacy response carried the raw chat-id array (its filter
// bug pushed every chat id), so group and user chats are both included.
func (h *Compat) ChatList(c fiber.Ctx) error {
	instance, err := h.resolveInstance(c.Query("session_name"))
	if err != nil {
		return nodeResponse(c, fiber.StatusNotFound, false, "Session not found.", nil)
	}
	ctx := whatsapp.ContextWithDevice(c.Context(), instance)

	jids := make([]string, 0)
	for offset := 0; ; offset += chatListPageSize {
		page, pageErr := h.chatSvc.ListChats(ctx, domainChat.ListChatsRequest{
			Limit:  chatListPageSize,
			Offset: offset,
		})
		if pageErr != nil {
			return nodeResponse(c, fiber.StatusInternalServerError, false, "Failed to get chat list.", nil)
		}
		for _, chat := range page.Data {
			jids = append(jids, chat.JID)
		}
		if len(page.Data) < chatListPageSize {
			break
		}
	}

	return nodeResponse(c, fiber.StatusOK, true, "", jids)
}

// ChatGet mirrors GET /api/v1/social/chat/get. Messages are returned in a
// Baileys-store-like shape (key/pushName/messageTimestamp/message) so
// legacy consumers keep parsing; media messages carry only the fields this
// service stores (content/caption, filename, URL) rather than the full
// protocol objects Baileys exposed. cursor_fromMe is mapped onto the stored
// from-me filter (an approximation of Baileys' before-cursor semantics), and
// limit is capped at the storage validation maximum of 100.
func (h *Compat) ChatGet(c fiber.Ctx) error {
	instance, err := h.resolveInstance(c.Query("session_name"))
	if err != nil {
		return nodeResponse(c, fiber.StatusNotFound, false, "Session not found.", nil)
	}
	ctx := whatsapp.ContextWithDevice(c.Context(), instance)

	limit := 25
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 100 {
		limit = 100
	}

	chatJID := strings.TrimSpace(c.Query("mobile"))
	utils.SanitizePhone(&chatJID)

	request := domainChat.GetChatMessagesRequest{ChatJID: chatJID, Limit: limit}
	if raw := c.Query("cursor_fromMe"); raw != "" {
		fromMe := raw == "true"
		request.IsFromMe = &fromMe
	}

	response, responseErr := h.chatSvc.GetChatMessages(ctx, request)
	if responseErr != nil {
		return nodeResponse(c, fiber.StatusInternalServerError, false, "Failed to get conversation.", nil)
	}

	messages := make([]fiber.Map, 0, len(response.Data))
	for _, m := range response.Data {
		messages = append(messages, baileysStyleMessage(m))
	}
	return nodeResponse(c, fiber.StatusOK, true, "", messages)
}

// baileysStyleMessage renders one stored message the way the legacy
// loadMessages rows looked to consumers: a key block, a unix-seconds
// messageTimestamp, and a message object whose content is keyed by type
// (Baileys nests media under e.g. message.documentMessage; text sits in
// message.conversation).
func baileysStyleMessage(m domainChat.MessageInfo) fiber.Map {
	var typeKey string
	var content fiber.Map
	switch m.MediaType {
	case "image", "video", "video_note":
		typeKey = "imageMessage"
		if m.MediaType == "video" || m.MediaType == "video_note" {
			typeKey = "videoMessage"
		}
		content = fiber.Map{"caption": m.Content, "url": m.URL}
	case "audio":
		typeKey = "audioMessage"
		content = fiber.Map{"url": m.URL}
	case "document":
		typeKey = "documentMessage"
		content = fiber.Map{"fileName": m.Filename, "caption": m.Content, "url": m.URL}
	case "sticker":
		typeKey = "stickerMessage"
		content = fiber.Map{"url": m.URL}
	default:
		typeKey = "conversation"
		content = nil
	}

	timestamp := int64(0)
	if t, err := time.Parse(time.RFC3339, m.Timestamp); err == nil {
		timestamp = t.Unix()
	}

	message := fiber.Map{}
	if typeKey == "conversation" {
		message["conversation"] = m.Content
	} else {
		message[typeKey] = content
	}

	return fiber.Map{
		"key": fiber.Map{
			"remoteJid":   m.ChatJID,
			"fromMe":      m.IsFromMe,
			"id":          m.ID,
			"participant": m.SenderJID,
		},
		"pushName":         m.SenderDisplayName,
		"messageTimestamp": timestamp,
		"message":          message,
	}
}
