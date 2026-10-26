package usecase

import (
	"context"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	projectSQLite "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/sqlite"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"path/filepath"
	"testing"
)

func TestSwitchPairingDisposesOldClientWithoutRemovingSlot(t *testing.T) {
	ctx := context.Background()
	uri := projectSQLite.FormatChatStorageURI("file:"+filepath.Join(t.TempDir(), "pairing.db"), false, true)
	db, err := sqlstore.New(ctx, projectSQLite.DriverName, uri, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dm := whatsapp.NewDeviceManager(db, db, nil)
	if _, err = dm.CreateDevice(ctx, "switch-test"); err != nil {
		t.Fatal(err)
	}
	svc := &serviceApp{deviceManager: dm}
	instance, old, err := svc.ensureClient(ctx, "switch-test")
	if err != nil {
		t.Fatal(err)
	}
	cancelled := false
	operation := newPairingOperation()
	operation.client = old
	operation.cancel = func() { cancelled = true }
	svc.pairings.Store("switch-test", operation)
	_, fresh, err := svc.replaceUnpairedClient(ctx, "switch-test", instance, old)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == old || !cancelled {
		t.Fatal("old pairing was reused")
	}
	if _, ok := dm.GetDevice("switch-test"); !ok {
		t.Fatal("slot removed")
	}
	if fresh.Store.ID != nil {
		t.Fatal("unlinked replacement unexpectedly paired")
	}
	if _, exists := svc.pairings.Load("switch-test"); exists {
		t.Fatal("old QR operation remained")
	}
}
