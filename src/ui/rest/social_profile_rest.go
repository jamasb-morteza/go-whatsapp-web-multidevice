package rest

import (
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/usecase"
	"github.com/gofiber/fiber/v3"
)

func (controller *User) SocialProfile(c fiber.Ctx) error {
	phone := c.Query("mobile")
	utils.SanitizePhone(&phone)
	ctx := whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c))
	result, status := usecase.SocialProfile(ctx, controller.Service, phone)
	return c.Status(status).JSON(result)
}
