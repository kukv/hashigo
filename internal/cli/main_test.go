package cli

import (
	"os"
	"testing"

	"github.com/kukv/hashigo/internal/testutil/fakebin"
)

func TestMain(m *testing.M) {
	if fakebin.Enabled() {
		fakebin.Main()
	}
	os.Exit(m.Run())
}
