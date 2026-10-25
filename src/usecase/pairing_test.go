package usecase

import (
	"context"
	domainApp "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/app"
	"testing"
	"time"
)

func TestPairingReturnsCurrentRotatedQR(t *testing.T) {
	p := newPairingOperation()
	p.update(domainApp.LoginResponse{Code: "first"}, time.Now().Add(time.Minute))
	p.update(domainApp.LoginResponse{Code: "second"}, time.Now().Add(20*time.Second))
	value, err := p.wait(context.Background())
	if err != nil || value.Code != "second" || value.Duration > 20 {
		t.Fatalf("stale QR: %+v %v", value, err)
	}
}
func TestPairingNeverReturnsExpiredQR(t *testing.T) {
	p := newPairingOperation()
	p.update(domainApp.LoginResponse{Code: "expired"}, time.Now().Add(-time.Second))
	p.finish()
	if _, err := p.wait(context.Background()); err == nil {
		t.Fatal("returned expired QR")
	}
}
func TestPairingWaitRespectsCancellation(t *testing.T) {
	p := newPairingOperation()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.wait(ctx); err == nil {
		t.Fatal("ignored cancellation")
	}
}
