package provision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerfile_SealedStaticOfflineContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testAssetsRoot(t), "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"# syntax=" + DockerfileFrontend,
		"ARG BASE_REF=" + BaseReference,
		"ARG SOURCE_DATE_EPOCH=946684800",
		"ADD --checksum=sha256:${TINYGO_SHA256} ${TINYGO_URL}",
		"LLVM version " + TinyGoLLVMVersion,
		"COPY launch/licenses/TinyGo-LICENSE /licenses/tinygo/LICENSE",
		"CGO_ENABLED=0 GOOS=linux GOARCH=amd64",
		"/usr/local/bin/buckley-launch-probe-v1",
		"/usr/local/bin/buckley-launch-supervisor-v1",
		ContractLabelKey + "=\"worker-v1\"",
		"GOTOOLCHAIN=local",
		"GOWORK=off",
		"GOFLAGS=-mod=readonly",
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOMODCACHE=/opt/buckley/modcache",
		"/usr/share/buckley-launch/module-inventory.tsv",
		"/opt/buckley/module-input/gsxmail",
		"/opt/buckley/module-input/gosx/editor",
		"/opt/buckley/module-input/gosx/cmd/buildbootstrap",
		"/opt/buckley/module-input/tqwebp/bench/deepteams",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Dockerfile missing sealed contract fragment %q", required)
		}
	}
	for _, forbidden := range []string{"COPY . ", "COPY .\n", "apt-get", "nodejs", "npm ", "chrome", "chromium", "danmuji", "go.work"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Fatalf("Dockerfile contains forbidden fragment %q", forbidden)
		}
	}
	if strings.Count(text, "FROM ${BASE_REF}") != 4 {
		t.Fatalf("Dockerfile base-stage count = %d, want 4 exact pinned stages", strings.Count(text, "FROM ${BASE_REF}"))
	}
}
