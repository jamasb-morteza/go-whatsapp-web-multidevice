package usecase

import (
	"context"
	"errors"
	user "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type profileStub struct {
	registered bool
	err        error
	avatarURL  string
	avatarErr  error
	calls      []string
}

func (s *profileStub) IsOnWhatsApp(_ context.Context, r user.CheckRequest) (user.CheckResponse, error) {
	s.calls = append(s.calls, r.Phone)
	return user.CheckResponse{IsOnWhatsApp: s.registered}, s.err
}
func (s *profileStub) Info(_ context.Context, r user.InfoRequest) (user.InfoResponse, error) {
	s.calls = append(s.calls, r.Phone)
	return user.InfoResponse{Data: []user.InfoResponseData{{Name: "Test Name"}}, ResolvedLID: "123@lid"}, nil
}
func (s *profileStub) Avatar(_ context.Context, r user.AvatarRequest) (user.AvatarResponse, error) {
	s.calls = append(s.calls, r.Phone)
	return user.AvatarResponse{URL: s.avatarURL, ID: "pic"}, s.avatarErr
}

func TestSocialProfileNotRegistered(t *testing.T) {
	s := &profileStub{}
	body, status := SocialProfile(context.Background(), s, "123")
	if status != 422 || body["error"].(map[string]any)["code"] != "NOT_ON_WHATSAPP" || len(s.calls) != 1 {
		t.Fatalf("unexpected: %d %v calls=%v", status, body, s.calls)
	}
}
func TestSocialProfileCheckFailureIsNotUnregistered(t *testing.T) {
	s := &profileStub{err: errors.New("device disconnected")}
	body, status := SocialProfile(context.Background(), s, "123")
	if status != 502 || body["error"].(map[string]any)["code"] == "NOT_ON_WHATSAPP" {
		t.Fatalf("unexpected: %d %v", status, body)
	}
}
func TestSocialProfilePrivateAvatarPreservesName(t *testing.T) {
	s := &profileStub{registered: true, avatarErr: errors.New("profile picture is private")}
	body, status := SocialProfile(context.Background(), s, "123")
	person := body["data"].(map[string]any)["user"].(map[string]any)
	if status != 200 || person["name"] != "Test Name" || person["avatar"].(map[string]any)["base64"] != "" {
		t.Fatalf("unexpected: %d %v", status, body)
	}
	for _, phone := range s.calls {
		if phone != "123" {
			t.Fatal(s.calls)
		}
	}
}
func TestSocialProfileDownloadsBase64(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte{'\x89', 'P', 'N', 'G', '\r', '\n', '\x1a', '\n', 0, 0, 0, 0})
	}))
	defer server.Close()
	s := &profileStub{registered: true, avatarURL: server.URL}
	body, status := SocialProfile(context.Background(), s, "123")
	avatar := body["data"].(map[string]any)["user"].(map[string]any)["avatar"].(map[string]any)
	if status != 200 || !strings.HasPrefix(avatar["base64"].(string), "data:image/png;base64,") {
		t.Fatalf("unexpected: %d %v", status, body)
	}
}
func TestSocialAvatarRejectsNonImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not an image")) }))
	defer server.Close()
	if _, err := socialAvatar(context.Background(), server.URL); err == nil {
		t.Fatal("accepted non-image")
	}
}

func TestSocialProfileFallbackPreserved(t *testing.T) {
	s := &profileStub{registered: true, avatarErr: errors.New("avatar unavailable")}
	body, status := SocialProfile(context.Background(), s, "123")
	avatar := body["data"].(map[string]any)["user"].(map[string]any)["avatar"].(map[string]any)
	if status != 200 || avatar["base64"] != socialProfileFallback {
		t.Fatal("fallback not preserved")
	}
}
func TestSocialProfileEmptyNumber(t *testing.T) {
	s := &profileStub{}
	_, status := SocialProfile(context.Background(), s, "")
	if status != 400 || len(s.calls) != 0 {
		t.Fatal("empty number reached WhatsApp")
	}
}
