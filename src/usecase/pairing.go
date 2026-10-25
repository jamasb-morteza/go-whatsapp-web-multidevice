package usecase

import (
	"context"
	"fmt"
	domainApp "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/app"
	"go.mau.fi/whatsmeow"
	"sync"
	"time"
)

// pairingOperation owns the rotating QR snapshot, independently of HTTP requests.
type pairingOperation struct {
	client  *whatsmeow.Client
	mu      sync.Mutex
	current domainApp.LoginResponse
	expires time.Time
	active  bool
	changed chan struct{}
}

func newPairingOperation() *pairingOperation {
	return &pairingOperation{active: true, changed: make(chan struct{})}
}
func (p *pairingOperation) update(value domainApp.LoginResponse, expires time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current = value
	p.expires = expires
	close(p.changed)
	p.changed = make(chan struct{})
}
func (p *pairingOperation) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active = false
	close(p.changed)
	p.changed = make(chan struct{})
}
func (p *pairingOperation) isActive() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.active }
func (p *pairingOperation) wait(ctx context.Context) (domainApp.LoginResponse, error) {
	for {
		p.mu.Lock()
		value, expires, active, changed := p.current, p.expires, p.active, p.changed
		p.mu.Unlock()
		remaining := time.Until(expires)
		if remaining > time.Second {
			value.Duration = time.Duration(remaining.Seconds())
			return value, nil
		}
		if !active {
			return domainApp.LoginResponse{}, fmt.Errorf("pairing expired; request a new QR")
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return domainApp.LoginResponse{}, ctx.Err()
		}
	}
}
