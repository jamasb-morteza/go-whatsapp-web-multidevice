package usecase

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainApp "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/app"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/websocket"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	fiberUtils "github.com/gofiber/utils/v2"
	"github.com/sirupsen/logrus"
	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type serviceApp struct {
	chatStorageRepo domainChatStorage.IChatStorageRepository
	deviceManager   *whatsapp.DeviceManager
	pairings        sync.Map
	phonePairings   sync.Map
	pairingLocks    [64]sync.Mutex
}

func NewAppService(chatStorageRepo domainChatStorage.IChatStorageRepository, deviceManager *whatsapp.DeviceManager) domainApp.IAppUsecase {
	return &serviceApp{
		chatStorageRepo: chatStorageRepo,
		deviceManager:   deviceManager,
	}
}

func (service *serviceApp) Login(ctx context.Context, deviceID string) (domainApp.LoginResponse, error) {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(deviceID))
	lock := &service.pairingLocks[hash.Sum32()%64]
	lock.Lock()
	defer lock.Unlock()
	instance, client, err := service.ensureClient(ctx, deviceID)
	if err != nil {
		return domainApp.LoginResponse{}, err
	}
	if client.IsLoggedIn() {
		instance.UpdateStateFromClient()
		return domainApp.LoginResponse{}, pkgError.ErrAlreadyLoggedIn
	}
	// A stored pairing reconnects; it must never enter the QR reset path.
	if client.Store != nil && client.Store.ID != nil {
		if !client.IsConnected() {
			if err := client.Connect(); err != nil {
				return domainApp.LoginResponse{}, err
			}
		}
		return domainApp.LoginResponse{}, pkgError.ErrSessionSaved
	}
	if _, wasCode := service.phonePairings.LoadAndDelete(deviceID); wasCode {
		instance, client, err = service.replaceUnpairedClient(ctx, deviceID, instance, client)
		if err != nil {
			return domainApp.LoginResponse{}, err
		}
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 20*time.Second)
	defer waitCancel()
	if existing, ok := service.pairings.Load(deviceID); ok {
		operation := existing.(*pairingOperation)
		if operation.client == client && operation.isActive() {
			return operation.wait(waitCtx)
		}
	}
	client.Disconnect()
	instance.ClearPasskeyState()
	qrCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	channel, err := client.GetQRChannel(qrCtx)
	if err != nil {
		cancel()
		return domainApp.LoginResponse{}, err
	}
	operation := newPairingOperation()
	operation.client = client
	operation.cancel = cancel
	service.pairings.Store(deviceID, operation)
	go func() {
		defer cancel()
		defer operation.finish()
		defer service.pairings.CompareAndDelete(deviceID, operation)
		for {
			select {
			case <-qrCtx.Done():
				return
			case event, ok := <-channel:
				if !ok {
					return
				}
				// Log only outcome metadata: QR codes and raw errors may contain secrets.
				logrus.WithFields(logrus.Fields{
					"device_id":  deviceID,
					"event":      event.Event,
					"error_type": fmt.Sprintf("%T", event.Error),
				}).Info("[PAIRING] QR lifecycle event")
				if event.Event != "code" {
					continue
				}
				path := fmt.Sprintf("%s/scan-qr-%s.png", config.PathQrCode, fiberUtils.UUIDv4())
				if err := qrcode.WriteFile(event.Code, qrcode.Medium, 512, path); err != nil {
					continue
				}
				operation.update(domainApp.LoginResponse{Code: event.Code, ImagePath: path}, time.Now().Add(event.Timeout))
				go func(path string, ttl time.Duration) { time.Sleep(ttl); _ = os.Remove(path) }(path, event.Timeout)
			}
		}
	}()
	if err := client.Connect(); err != nil {
		cancel()
		return domainApp.LoginResponse{}, err
	}
	instance.UpdateStateFromClient()
	return operation.wait(waitCtx)
}

func (service *serviceApp) replaceUnpairedClient(ctx context.Context, deviceID string, instance *whatsapp.DeviceInstance, client *whatsmeow.Client) (*whatsapp.DeviceInstance, *whatsmeow.Client, error) {
	// Pairing timers belong to the old client. A fresh unlinked client prevents
	// their eventual Disconnect from interrupting the newly selected method.
	if operation, ok := service.pairings.LoadAndDelete(deviceID); ok {
		if cancel := operation.(*pairingOperation).cancel; cancel != nil {
			cancel()
		}
	}
	client.Disconnect()
	instance.SetClient(nil)
	return service.ensureClient(ctx, deviceID)
}

func (service *serviceApp) LoginWithCode(ctx context.Context, deviceID string, phoneNumber string) (loginCode string, err error) {
	if err = validations.ValidateLoginWithCode(ctx, phoneNumber); err != nil {
		return "", err
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(deviceID))
	lock := &service.pairingLocks[hash.Sum32()%64]
	lock.Lock()
	defer lock.Unlock()
	instance, client, err := service.ensureClient(ctx, deviceID)
	if err != nil {
		return "", err
	}
	if client.IsLoggedIn() {
		return "", pkgError.ErrAlreadyLoggedIn
	}
	if client.Store != nil && client.Store.ID != nil {
		return "", pkgError.ErrSessionSaved
	}
	service.phonePairings.Delete(deviceID)
	instance, client, err = service.replaceUnpairedClient(ctx, deviceID, instance, client)
	if err != nil {
		return "", err
	}
	if err = client.Connect(); err != nil {
		return "", err
	}
	loginCode, err = client.PairPhone(ctx, phoneNumber, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		return "", err
	}
	service.phonePairings.Store(deviceID, true)
	instance.UpdateStateFromClient()
	logrus.WithField("device_id", deviceID).Info("[PAIRING] phone code generated")
	return loginCode, nil
}

func (service *serviceApp) PasskeyChallenge(_ context.Context, deviceID string) (response domainApp.PasskeyChallengeResponse, err error) {
	if service.deviceManager == nil {
		return response, fmt.Errorf("device manager not initialized")
	}

	instance, ok := service.deviceManager.GetDevice(deviceID)
	if !ok || instance == nil {
		return response, fmt.Errorf("device %s not found", deviceID)
	}

	challenge, code, skipHandoffUX := instance.PasskeyState()
	response.Status = "none"
	if challenge != nil {
		response.Status = "awaiting_response"
	} else if code != "" {
		response.Status = "awaiting_confirmation"
	}
	response.Challenge = challenge
	response.Code = code
	response.SkipHandoffUX = skipHandoffUX
	return response, nil
}

func (service *serviceApp) PasskeyResponse(ctx context.Context, deviceID string, assertion *types.WebAuthnResponse) error {
	if err := validations.ValidatePasskeyResponse(ctx, assertion); err != nil {
		return err
	}

	if service.deviceManager == nil {
		return fmt.Errorf("device manager not initialized")
	}

	instance, ok := service.deviceManager.GetDevice(deviceID)
	if !ok || instance == nil {
		return fmt.Errorf("device %s not found", deviceID)
	}

	client := instance.GetClient()
	if client == nil {
		return pkgError.ErrWaCLI
	}

	challenge, _, _ := instance.PasskeyState()
	if challenge == nil {
		return fmt.Errorf("no pending passkey pairing request for device %s", deviceID)
	}

	if !client.IsConnected() {
		return fmt.Errorf("device %s is not connected, restart login and retry the passkey flow", deviceID)
	}

	if err := client.SendPasskeyResponse(ctx, assertion); err != nil {
		logrus.Errorf("[PASSKEY][%s] failed to send passkey response: %v", deviceID, err)
		return err
	}

	// The PairPasskeyConfirmation event repopulates the confirmation code shortly after.
	instance.ClearPasskeyState()
	return nil
}

func (service *serviceApp) PasskeyConfirm(ctx context.Context, deviceID string) error {
	if service.deviceManager == nil {
		return fmt.Errorf("device manager not initialized")
	}

	instance, ok := service.deviceManager.GetDevice(deviceID)
	if !ok || instance == nil {
		return fmt.Errorf("device %s not found", deviceID)
	}

	client := instance.GetClient()
	if client == nil {
		return pkgError.ErrWaCLI
	}

	_, code, _ := instance.PasskeyState()
	if code == "" {
		return fmt.Errorf("no pending passkey confirmation for device %s", deviceID)
	}

	if !client.IsConnected() {
		return fmt.Errorf("device %s is not connected, restart login and retry the passkey flow", deviceID)
	}

	if err := client.SendPasskeyConfirmation(ctx); err != nil {
		logrus.Errorf("[PASSKEY][%s] failed to send passkey confirmation: %v", deviceID, err)
		return err
	}

	instance.ClearPasskeyState()
	return nil
}

func (service *serviceApp) Logout(ctx context.Context, deviceID string) error {
	if err := validations.ValidateDeviceID(ctx, deviceID); err != nil {
		return err
	}
	if service.deviceManager == nil {
		return fmt.Errorf("device manager not initialized")
	}

	if err := service.deviceManager.LogoutDeviceKeepSlot(ctx, deviceID); err != nil {
		logrus.WithError(err).Warnf("[LOGOUT][%s] logout completed with warnings", deviceID)
		return err
	}

	// Broadcast the logout so the UI can refresh without manual polling. The slot is
	// kept, so the device stays listed (disconnected) and can be re-paired by id.
	var devices []domainApp.DevicesResponse
	if list, err := service.FetchDevices(ctx); err == nil {
		devices = list
	} else {
		logrus.WithError(err).Warn("[LOGOUT] failed to fetch devices after logout")
	}

	websocket.Broadcast <- websocket.BroadcastMessage{
		Code:    "DEVICE_LOGGED_OUT",
		Message: fmt.Sprintf("Device %s logged out (slot kept)", deviceID),
		Result: map[string]any{
			"device_id": deviceID,
			"devices":   devices,
		},
	}

	return nil
}

func (service *serviceApp) Reconnect(_ context.Context, deviceID string) (err error) {
	instance, client, err := service.ensureClient(context.Background(), deviceID)
	if err != nil {
		return err
	}

	if client.Store == nil || client.Store.ID == nil {
		return fmt.Errorf("device %s is not logged in (session deleted)", deviceID)
	}

	client.Disconnect()
	err = client.Connect()
	instance.UpdateStateFromClient()
	if err != nil {
		logrus.Errorf("[RECONNECT][%s] Reconnect failed: %v", deviceID, err)
	}
	return err
}

func (service *serviceApp) Status(_ context.Context, deviceID string) (bool, bool, error) {
	if service.deviceManager == nil {
		return false, false, fmt.Errorf("device manager not initialized")
	}

	instance, ok := service.deviceManager.GetDevice(deviceID)
	if !ok || instance == nil {
		return false, false, fmt.Errorf("device %s not found", deviceID)
	}

	instance.UpdateStateFromClient()
	client := instance.GetClient()
	if client == nil {
		return false, false, nil
	}

	if client.Store == nil || client.Store.ID == nil {
		return false, false, nil
	}

	return client.IsConnected(), client.IsLoggedIn(), nil
}

func (service *serviceApp) FirstDevice(ctx context.Context) (response domainApp.DevicesResponse, err error) {
	devices, err := service.FetchDevices(ctx)
	if err != nil {
		return response, err
	}
	if len(devices) == 0 {
		return response, fmt.Errorf("no devices available")
	}
	return devices[0], nil
}

func (service *serviceApp) FetchDevices(_ context.Context) (response []domainApp.DevicesResponse, err error) {
	if service.deviceManager == nil {
		return response, fmt.Errorf("device manager not initialized")
	}

	for _, inst := range service.deviceManager.ListDevices() {
		inst.UpdateStateFromClient()
		name := inst.DisplayName()
		if name == "" {
			name = inst.PhoneNumber()
		}

		response = append(response, domainApp.DevicesResponse{
			Name:   name,
			Device: inst.ID(),
			JID:    inst.JID(),
		})
	}

	return response, nil
}

func (service *serviceApp) ensureClient(ctx context.Context, deviceID string) (*whatsapp.DeviceInstance, *whatsmeow.Client, error) {
	if deviceID == "" {
		return nil, nil, fmt.Errorf("device id is required")
	}

	if service.deviceManager == nil {
		return nil, nil, fmt.Errorf("device manager not initialized")
	}

	instance, err := service.deviceManager.EnsureClient(ctx, deviceID)
	if err != nil {
		return nil, nil, err
	}

	client := instance.GetClient()
	if client == nil {
		return instance, nil, pkgError.ErrWaCLI
	}

	return instance, client, nil
}
