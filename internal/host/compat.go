package host

import "runtime"

const ValidatedMacVersion = "26.1117.41037"
const ValidatedWindowsVersion = "26.1117.41037"
const WindowsStatus = "windows_phase0b_verified"

func VersionSupported(version string) bool {
	if runtime.GOOS == "windows" {
		return runtime.GOARCH == "amd64" && version == ValidatedWindowsVersion
	}
	return runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" && version == ValidatedMacVersion
}

func CompatibilityLabel() string {
	if runtime.GOOS == "windows" {
		return "Windows 已验证版本：" + ValidatedWindowsVersion
	}
	return "macOS 已验证版本：" + ValidatedMacVersion
}
