package provision

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"strings"
)

const (
	ToolchainSchema    = "buckley.launch.toolchain.v1"
	Platform           = "linux/amd64"
	BaseReference      = "golang:1.26-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36"
	BaseImageID        = "sha256:df664c2b56a98910721a529a9a74e20181c607ac32528e758a1dcfd522a9f011"
	GoVersion          = "1.26.6"
	TinyGoVersion      = "0.41.1"
	TinyGoLLVMVersion  = "20.1.1"
	TinyGoURL          = "https://github.com/tinygo-org/tinygo/releases/download/v0.41.1/tinygo0.41.1.linux-amd64.tar.gz"
	TinyGoSHA256       = "e156d1d93a376eef639a4143d13be07e8c463fb6cf2d7d447698ed4474d23e91"
	TinyGoLicense      = "https://raw.githubusercontent.com/tinygo-org/tinygo/v0.41.1/LICENSE"
	TinyGoLicenseSH    = "4cb7d99a97ebd57584ea8398898c4b0bbbcb39662330712b43153efdad308766"
	DockerfileFrontend = "docker/dockerfile:1.12@sha256:93bfd3b68c109427185cd78b4779fc82b484b0b7618e36d0f104d4d801e66d25"
	BuildxVersion      = "v0.28.0"
	BuildxCommit       = "b1281b81bba797b21d9eaf256e6a13eb14419836"
	BuildKitVersion    = "v0.24.0"
	BuildKitDriver     = "docker"
	SourceDateEpoch    = int64(946684800)
	RegistryRef        = "docker.io/library/registry@sha256:6c5666b861f3505b116bb9aa9b25175e71210414bd010d92035ff64018f9457e"
	RegistryVersion    = "3.0.0"
	RegistryLicense    = "Apache-2.0"

	maxToolchainLockBytes = 16 << 10
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type ToolchainLock struct {
	Schema              string `json:"schema"`
	Platform            string `json:"platform"`
	BaseRef             string `json:"base_ref"`
	BaseImageID         string `json:"base_image_id"`
	GoVersion           string `json:"go_version"`
	TinyGoVersion       string `json:"tinygo_version"`
	TinyGoLLVMVersion   string `json:"tinygo_llvm_version"`
	TinyGoURL           string `json:"tinygo_url"`
	TinyGoSHA256        string `json:"tinygo_sha256"`
	TinyGoLicenseURL    string `json:"tinygo_license_url"`
	TinyGoLicenseSHA256 string `json:"tinygo_license_sha256"`
	DockerfileFrontend  string `json:"dockerfile_frontend"`
	BuildxVersion       string `json:"buildx_version"`
	BuildxCommit        string `json:"buildx_commit"`
	BuildKitVersion     string `json:"buildkit_version"`
	BuildKitDriver      string `json:"buildkit_driver"`
	SourceDateEpoch     int64  `json:"source_date_epoch"`
	RegistryRef         string `json:"registry_ref"`
	RegistryVersion     string `json:"registry_version"`
	RegistryLicense     string `json:"registry_license"`
}

func ExpectedToolchain() ToolchainLock {
	return ToolchainLock{
		Schema:              ToolchainSchema,
		Platform:            Platform,
		BaseRef:             BaseReference,
		BaseImageID:         BaseImageID,
		GoVersion:           GoVersion,
		TinyGoVersion:       TinyGoVersion,
		TinyGoLLVMVersion:   TinyGoLLVMVersion,
		TinyGoURL:           TinyGoURL,
		TinyGoSHA256:        TinyGoSHA256,
		TinyGoLicenseURL:    TinyGoLicense,
		TinyGoLicenseSHA256: TinyGoLicenseSH,
		DockerfileFrontend:  DockerfileFrontend,
		BuildxVersion:       BuildxVersion,
		BuildxCommit:        BuildxCommit,
		BuildKitVersion:     BuildKitVersion,
		BuildKitDriver:      BuildKitDriver,
		SourceDateEpoch:     SourceDateEpoch,
		RegistryRef:         RegistryRef,
		RegistryVersion:     RegistryVersion,
		RegistryLicense:     RegistryLicense,
	}
}

func LoadToolchainLock(path string) (ToolchainLock, string, error) {
	data, err := readStableRegular(path, maxToolchainLockBytes)
	if err != nil {
		return ToolchainLock{}, "", errors.New("toolchain lock unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var lock ToolchainLock
	if err := decoder.Decode(&lock); err != nil {
		return ToolchainLock{}, "", errors.New("toolchain lock is invalid")
	}
	if err := rejectTrailingJSON(decoder); err != nil || lock != ExpectedToolchain() {
		return ToolchainLock{}, "", errors.New("toolchain lock does not match the sealed contract")
	}
	digest := sha256.Sum256(data)
	return lock, hex.EncodeToString(digest[:]), nil
}

func (l ToolchainLock) ValidateRuntime() error {
	if l != ExpectedToolchain() {
		return errors.New("toolchain contract mismatch")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("sealed launch provisioning is unavailable on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if !sha256Pattern.MatchString(l.TinyGoSHA256) || !sha256Pattern.MatchString(l.TinyGoLicenseSHA256) {
		return errors.New("toolchain checksums are invalid")
	}
	return nil
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return errors.New("unexpected trailing JSON")
}

func readStableRegular(path string, maxBytes int64) ([]byte, error) {
	if path == "" || maxBytes <= 0 {
		return nil, errors.New("invalid stable read")
	}
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || regularLinkCount(before) > 1 || before.Size() < 0 || before.Size() > maxBytes {
		return nil, errors.New("file is unavailable")
	}
	read := func() ([]byte, os.FileInfo, error) {
		file, err := os.Open(path)
		if err != nil {
			return nil, nil, err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || regularLinkCount(info) > 1 || !os.SameFile(before, info) || info.Size() > maxBytes {
			return nil, nil, errors.New("file changed")
		}
		data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
		if err != nil || int64(len(data)) != info.Size() || int64(len(data)) > maxBytes {
			return nil, nil, errors.New("file read is invalid")
		}
		return data, info, nil
	}
	first, firstInfo, err := read()
	if err != nil {
		return nil, err
	}
	second, secondInfo, err := read()
	if err != nil || !os.SameFile(firstInfo, secondInfo) || firstInfo.Size() != secondInfo.Size() || !firstInfo.ModTime().Equal(secondInfo.ModTime()) || !bytes.Equal(first, second) {
		return nil, errors.New("file changed during read")
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || regularLinkCount(after) > 1 || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("file changed during read")
	}
	return first, nil
}

func validDigest(value string) bool {
	return sha256Pattern.MatchString(strings.TrimPrefix(value, "sha256:"))
}
