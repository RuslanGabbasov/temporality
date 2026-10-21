package sandbox

import (
	"strings"
	"testing"
)

// D4 (AC-6): an apk «no such package» refusal becomes an error that names
// the package, the base image and how to check the name — over the raw log.
func TestPackageInstallFailureNamesMissingPackage(t *testing.T) {
	def, err := CustomDefinition("qa-tools", "python", []string{"sqlmap"})
	if err != nil {
		t.Fatal(err)
	}
	log := "#6 [3/3] RUN apk add --no-cache sqlmap\n#6 0.725 ERROR: unable to select packages:\n#6 0.725 sqlmap (no such package):\n"
	failure := packageInstallFailure(def, log)
	if failure == nil {
		t.Fatal("expected an actionable failure")
	}
	msg := failure.Error()
	for _, want := range []string{`пакет "sqlmap"`, "не найден в репозиториях apk", "python:3.12-alpine", "проверьте имя пакета", "sqlmap (no such package)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("failure missing %q:\n%s", want, msg)
		}
	}
}

// The apt↔apk alias hint fires when the missing name belongs to the other
// distro's index — for apk failures from the Debian map, for apt from the
// reverse one.
func TestPackageInstallFailureSuggestsAlias(t *testing.T) {
	def, _ := CustomDefinition("op-recipe", "python", nil)
	apkLog := "ERROR: unable to select packages:\n  golang-go (no such package):\n"
	failure := packageInstallFailure(def, apkLog)
	if failure == nil || !strings.Contains(failure.Error(), `"go"`) || !strings.Contains(failure.Error(), "имя другой пакетной системы") {
		t.Fatalf("apk alias hint missing: %v", failure)
	}

	browser, _ := CustomDefinition("browser-tests", "browser-e2e", nil)
	aptLog := "Err:1 http://deb.debian.org/debian bookworm InRelease\nE: Unable to locate package build-base\n"
	failure = packageInstallFailure(browser, aptLog)
	for _, want := range []string{`"build-essential"`, "репозиториях apt", "playwright:v1.49.0-jammy"} {
		if failure == nil || !strings.Contains(failure.Error(), want) {
			t.Fatalf("apt failure missing %q: %v", want, failure)
		}
	}
}

// Unrelated build failures keep the plain raw-log error path.
func TestPackageInstallFailureIgnoresUnrelatedBuildErrors(t *testing.T) {
	def, _ := CustomDefinition("qa-tools", "python", nil)
	for _, log := range []string{
		"failed to solve: mcr.microsoft.com/playwright:v1.49.0-jammy: manifest not found",
		"ERROR: unable to select packages:", // refusal without a named package
		"executor failed running /bin/sh -c apk add",
	} {
		if failure := packageInstallFailure(def, log); failure != nil {
			t.Errorf("log %q must pass through unmapped, got: %v", log, failure)
		}
	}
}
