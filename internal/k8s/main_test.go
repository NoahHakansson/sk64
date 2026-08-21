package k8s

import (
	"fmt"
	"os"
	"testing"

	"github.com/NoahHakansson/sk64/internal/kubetest"
)

func TestMain(m *testing.M) {
	if err := kubetest.IsolateAmbientCluster(); err != nil {
		fmt.Fprintf(os.Stderr, "isolate ambient cluster: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
