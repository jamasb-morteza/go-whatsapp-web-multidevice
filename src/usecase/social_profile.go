package usecase

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	user "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
)

type socialProfileSource interface {
	IsOnWhatsApp(context.Context, user.CheckRequest) (user.CheckResponse, error)
	Info(context.Context, user.InfoRequest) (user.InfoResponse, error)
	Avatar(context.Context, user.AvatarRequest) (user.AvatarResponse, error)
}

// SocialProfile preserves the contact lookup envelope previously assembled in n8n.
func SocialProfile(ctx context.Context, source socialProfileSource, phone string) (map[string]any, int) {
	fail := func(code string, err error, status int) (map[string]any, int) {
		return map[string]any{"success": false, "error": map[string]any{"code": code, "message": err.Error()}}, status
	}
	if phone == "" {
		return fail("INVALID_PHONE", fmt.Errorf("mobile is required"), 400)
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	check, err := source.IsOnWhatsApp(ctx, user.CheckRequest{Phone: phone})
	if err != nil {
		return fail("CONTACT_CHECK_FAILED", err, 502)
	}
	if !check.IsOnWhatsApp {
		return fail("NOT_ON_WHATSAPP", fmt.Errorf("The supplied mobile number is not registered on WhatsApp."), 422)
	}
	info, err := source.Info(ctx, user.InfoRequest{Phone: phone})
	if err != nil {
		return fail("USER_INFO_FAILED", err, 502)
	}
	if len(info.Data) == 0 {
		return fail("USER_INFO_NOT_FOUND", fmt.Errorf("User information was not returned."), 422)
	}
	avatar, avatarErr := source.Avatar(ctx, user.AvatarRequest{Phone: phone})
	image := ""
	if avatarErr == nil && avatar.URL != "" {
		image, avatarErr = socialAvatar(ctx, avatar.URL)
	}
	avatarMeta := map[string]any{"success": avatarErr == nil && image != "", "fallback": false, "code": "SUCCESS", "message": "Success get avatar"}
	if avatarErr != nil {
		avatarMeta["code"] = "AVATAR_UNAVAILABLE"
		avatarMeta["message"] = avatarErr.Error()
		message := strings.ToLower(avatarErr.Error())
		if !strings.Contains(message, "privacy") && !strings.Contains(message, "private") && !strings.Contains(message, "hidden") {
			image = socialProfileFallback
			avatarMeta["fallback"] = true
		}
	}
	person := info.Data[0]
	return map[string]any{
		"success": true,
		"data": map[string]any{"user": map[string]any{
			"name": person.Name, "verified_name": person.VerifiedName, "status": person.Status,
			"picture_id": person.PictureID, "devices": person.Devices, "resolved_lid": info.ResolvedLID,
			"avatar": map[string]any{"id": avatar.ID, "type": avatar.Type, "base64": image},
		}},
		"meta": map[string]any{"user_info": map[string]any{"success": true, "code": "SUCCESS", "message": "Success get user info"}, "avatar": avatarMeta},
	}, 200
}

func socialAvatar(ctx context.Context, address string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", fmt.Errorf("avatar download returned HTTP %d", response.StatusCode)
	}
	const maxBytes = 5 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) == 0 || len(data) > maxBytes {
		return "", fmt.Errorf("avatar is empty or exceeds 5 MiB")
	}
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		return "", fmt.Errorf("avatar is not an image")
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
