package utils

import (
	"strings"
)

type VersionComparisonResult string

const (
	VersionLower  VersionComparisonResult = "lower"
	VersionEqual  VersionComparisonResult = "equal"
	VersionHigher VersionComparisonResult = "higher"
)

func CompareFirmwareVersions(stateVersion, specVersion string) (VersionComparisonResult, error) {
	//returns a comparison result of the stateVersion and specVersion.
	// If stateVersion is lower than specVersion, it returns VersionLower.
	// If stateVersion is equal to specVersion, it returns VersionEqual.
	// If stateVersion is higher than specVersion, it returns VersionHigher.
	specParts := normalizeVersion(specVersion)
	stateParts := normalizeVersion(stateVersion)

	for i := 0; i < len(specParts) && i < len(stateParts); i++ {
		if stateParts[i] < specParts[i] {
			return VersionLower, nil
		} else if stateParts[i] > specParts[i] {
			return VersionHigher, nil
		}
	}

	if len(stateParts) < len(specParts) {
		return VersionLower, nil
	} else if len(stateParts) > len(specParts) {
		return VersionHigher, nil
	}

	return VersionEqual, nil
}

func normalizeVersion(version string) []string {
	version = strings.TrimSpace(version)
	if strings.Contains(version, " ") {
		parts := strings.SplitN(version, " ", 2)
		version = parts[1]
	}
	return strings.Split(version, ".")
}
