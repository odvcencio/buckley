# syntax=docker/dockerfile:1.12@sha256:93bfd3b68c109427185cd78b4779fc82b484b0b7618e36d0f104d4d801e66d25

ARG BASE_REF=golang:1.26-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36
ARG SOURCE_DATE_EPOCH=946684800

FROM ${BASE_REF} AS launch-tools
ARG TARGETOS
ARG TARGETARCH
RUN test "${TARGETOS}" = linux && test "${TARGETARCH}" = amd64
WORKDIR /src/launch
COPY launch/go.mod ./go.mod
COPY launch/cmd/probe ./cmd/probe
COPY launch/cmd/supervisor ./cmd/supervisor
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o /out/buckley-launch-probe-v1 ./cmd/probe \
 && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o /out/buckley-launch-supervisor-v1 ./cmd/supervisor

FROM ${BASE_REF} AS tinygo
ARG TINYGO_URL
ARG TINYGO_SHA256
ADD --checksum=sha256:${TINYGO_SHA256} ${TINYGO_URL} /tmp/tinygo.tar.gz
COPY launch/licenses/TinyGo-LICENSE /licenses/tinygo/LICENSE
RUN mkdir -p /opt/tinygo \
 && tar -xzf /tmp/tinygo.tar.gz --strip-components=1 -C /opt/tinygo \
 && rm -f /tmp/tinygo.tar.gz \
 && test "$(/opt/tinygo/bin/tinygo version | awk '{print $3}')" = 0.41.1 \
 && /opt/tinygo/bin/tinygo version | grep -F 'LLVM version 20.1.1' >/dev/null

FROM ${BASE_REF} AS modules
ENV GOTOOLCHAIN=local \
    GOWORK=off \
	GOFLAGS=-mod=readonly \
    GOMODCACHE=/opt/buckley/modcache
COPY modules /opt/buckley/module-input
RUN set -eu; \
	: > /opt/buckley/module-inventory.tsv; \
    for module_dir in \
      /opt/buckley/module-input/gsxmail \
      /opt/buckley/module-input/gosx \
      /opt/buckley/module-input/gosx/editor \
      /opt/buckley/module-input/gosx/cmd/buildbootstrap \
      /opt/buckley/module-input/tqwebp \
      /opt/buckley/module-input/tqwebp/bench/deepteams; do \
		(cd "${module_dir}" \
		 && GOPROXY=https://proxy.golang.org,direct GOSUMDB=sum.golang.org go mod download all \
		 && go list -m -f '{{if .Replace}}{{if .Replace.Version}}{{.Replace.Path}}{{"\t"}}{{.Replace.Version}}{{end}}{{else}}{{if .Version}}{{.Path}}{{"\t"}}{{.Version}}{{end}}{{end}}' all >> /opt/buckley/module-inventory.tsv); \
    done; \
	sed -i '/^$/d' /opt/buckley/module-inventory.tsv; \
	sort -u -o /opt/buckley/module-inventory.tsv /opt/buckley/module-inventory.tsv; \
    rm -rf /opt/buckley/module-input; \
	chmod -R a+rX /opt/buckley/modcache; \
	chmod 0444 /opt/buckley/module-inventory.tsv

FROM ${BASE_REF} AS worker
ARG MODULE_LOCK_SHA256
ARG TOOLCHAIN_LOCK_SHA256
ARG BASE_REF
ARG BASE_IMAGE_ID
ARG GO_VERSION
ARG TINYGO_VERSION
LABEL dev.m31labs.buckley.launch.contract="worker-v1" \
      dev.m31labs.buckley.launch.probe="/usr/local/bin/buckley-launch-probe-v1" \
      dev.m31labs.buckley.launch.supervisor="/usr/local/bin/buckley-launch-supervisor-v1" \
      dev.m31labs.buckley.launch.go-version="${GO_VERSION}" \
      dev.m31labs.buckley.launch.tinygo-version="${TINYGO_VERSION}" \
      dev.m31labs.buckley.launch.base="${BASE_IMAGE_ID}" \
      dev.m31labs.buckley.launch.module-lock="sha256:${MODULE_LOCK_SHA256}" \
      dev.m31labs.buckley.launch.toolchain-lock="sha256:${TOOLCHAIN_LOCK_SHA256}"
COPY --from=launch-tools /out/buckley-launch-probe-v1 /usr/local/bin/buckley-launch-probe-v1
COPY --from=launch-tools /out/buckley-launch-supervisor-v1 /usr/local/bin/buckley-launch-supervisor-v1
COPY --from=tinygo /opt/tinygo /opt/tinygo
COPY --from=tinygo /licenses/tinygo/LICENSE /usr/share/doc/tinygo/LICENSE
COPY --from=modules /opt/buckley/modcache /opt/buckley/modcache
COPY --from=modules /opt/buckley/module-inventory.tsv /usr/share/buckley-launch/module-inventory.tsv
RUN rm -rf /go/pkg/mod /root/.cache /root/.config /root/.gitconfig \
 && test "$(go version | awk '{print $3}')" = "go${GO_VERSION}" \
 && test "$(/opt/tinygo/bin/tinygo version | awk '{print $3}')" = "${TINYGO_VERSION}" \
 && git --version \
 && make --version >/dev/null \
 && gcc --version >/dev/null
ENV PATH="/opt/tinygo/bin:/usr/local/go/bin:${PATH}" \
    GOTOOLCHAIN=local \
    GOWORK=off \
    GOPROXY=off \
    GOSUMDB=off \
    GOMODCACHE=/opt/buckley/modcache
WORKDIR /workspace
ENTRYPOINT ["/bin/sleep"]
CMD ["infinity"]
