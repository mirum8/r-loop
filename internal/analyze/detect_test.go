package analyze

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func touch(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func executable(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, rel), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func detect(t *testing.T, root string) []Module {
	t.Helper()
	mods, err := Detect(root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	return mods
}

func TestDetectFindsAGoModuleInEachGoModDirectory(t *testing.T) {
	// given
	root := t.TempDir()
	touch(t, root, "go.mod", "tools/go.mod")

	// when
	actual := detect(t, root)

	// then
	expected := []Module{
		{Kind: Go, Dir: root, BuildRoot: root, Runner: "go"},
		{Kind: Go, Dir: filepath.Join(root, "tools"), BuildRoot: filepath.Join(root, "tools"), Runner: "go"},
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %+v, expected %+v", actual, expected)
	}
}

func TestDetectGivesEachMavenModuleTheOutermostPomAndItsWrapper(t *testing.T) {
	// given
	root := t.TempDir()
	touch(t, root, "pom.xml", "api/pom.xml", "api/core/pom.xml")
	executable(t, root, "mvnw")

	// when
	actual := detect(t, root)

	// then
	mvnw := filepath.Join(root, "mvnw")
	expected := []Module{
		{Kind: Maven, Dir: root, BuildRoot: root, Runner: mvnw},
		{Kind: Maven, Dir: filepath.Join(root, "api"), BuildRoot: root, Runner: mvnw},
		{Kind: Maven, Dir: filepath.Join(root, "api", "core"), BuildRoot: root, Runner: mvnw},
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %+v, expected %+v", actual, expected)
	}
}

func TestDetectRunsMavenFromPathWithoutAWrapper(t *testing.T) {
	// given
	root := t.TempDir()
	touch(t, root, "pom.xml", "api/pom.xml")

	// when
	actual := detect(t, root)

	// then
	expected := []Module{
		{Kind: Maven, Dir: root, BuildRoot: root, Runner: "mvn"},
		{Kind: Maven, Dir: filepath.Join(root, "api"), BuildRoot: root, Runner: "mvn"},
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %+v, expected %+v", actual, expected)
	}
}

func TestDetectGivesEachGradleModuleTheOutermostSettingsDirectoryAndItsWrapper(t *testing.T) {
	// given
	root := t.TempDir()
	touch(t, root, "settings.gradle.kts", "app/build.gradle.kts", "lib/build.gradle")
	executable(t, root, "gradlew")

	// when
	actual := detect(t, root)

	// then
	gradlew := filepath.Join(root, "gradlew")
	expected := []Module{
		{Kind: Gradle, Dir: filepath.Join(root, "app"), BuildRoot: root, Runner: gradlew},
		{Kind: Gradle, Dir: filepath.Join(root, "lib"), BuildRoot: root, Runner: gradlew},
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %+v, expected %+v", actual, expected)
	}
}

func TestDetectFallsBackToTheModuleDirectoryAndGradleOnPath(t *testing.T) {
	// given
	root := t.TempDir()
	touch(t, root, "svc/build.gradle")

	// when
	actual := detect(t, root)

	// then
	svc := filepath.Join(root, "svc")
	expected := []Module{{Kind: Gradle, Dir: svc, BuildRoot: svc, Runner: "gradle"}}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %+v, expected %+v", actual, expected)
	}
}

func TestDetectCountsAModuleWithBothGradleScriptsOnce(t *testing.T) {
	// given
	root := t.TempDir()
	touch(t, root, "build.gradle", "build.gradle.kts")

	// when
	actual := detect(t, root)

	// then
	expected := []Module{{Kind: Gradle, Dir: root, BuildRoot: root, Runner: "gradle"}}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %+v, expected %+v", actual, expected)
	}
}

func TestDetectSkipsDirectoriesThatHoldNoProjectModules(t *testing.T) {
	for _, dir := range []string{".git", ".r-loop", "vendor", "node_modules", "testdata"} {
		t.Run(dir, func(t *testing.T) {
			// given
			root := t.TempDir()
			touch(t, root, "go.mod", dir+"/x/go.mod", dir+"/pom.xml")

			// when
			actual := detect(t, root)

			// then
			expected := []Module{{Kind: Go, Dir: root, BuildRoot: root, Runner: "go"}}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("actual %+v, expected %+v", actual, expected)
			}
		})
	}
}

func TestDetectReturnsNoModulesForATreeWithoutMarkers(t *testing.T) {
	// given
	root := t.TempDir()
	touch(t, root, "README.md")

	// when
	actual, err := Detect(root)

	// then
	if err != nil || actual == nil || len(actual) != 0 {
		t.Fatalf("actual %#v, err %v; expected an empty slice", actual, err)
	}
}

func TestDetectFailsForAMissingRoot(t *testing.T) {
	// given
	root := filepath.Join(t.TempDir(), "gone")

	// when
	_, err := Detect(root)

	// then
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err %v, expected fs.ErrNotExist", err)
	}
}

func TestTouchedMapsEachFileToItsNearestModule(t *testing.T) {
	// given
	root := "/repo"
	rootMod := Module{Kind: Go, Dir: root, BuildRoot: root, Runner: "go"}
	tools := Module{Kind: Go, Dir: "/repo/tools", BuildRoot: "/repo/tools", Runner: "go"}

	// when
	actual := Touched([]Module{rootMod, tools}, root, []string{"main.go", "tools/gen.go"})

	// then
	expected := []Touch{{Module: rootMod, Files: []string{"main.go"}}, {Module: tools, Files: []string{"tools/gen.go"}}}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %+v, expected %+v", actual, expected)
	}
}

func TestTouchedCountsAGoModulesOwnGoModAndGoSum(t *testing.T) {
	// given
	rootMod := Module{Kind: Go, Dir: "/repo", BuildRoot: "/repo", Runner: "go"}
	tools := Module{Kind: Go, Dir: "/repo/tools", BuildRoot: "/repo/tools", Runner: "go"}

	// when
	actual := Touched([]Module{rootMod, tools}, "/repo", []string{"tools/go.mod", "tools/go.sum"})

	// then
	expected := []Touch{{Module: tools, Files: []string{"tools/go.mod", "tools/go.sum"}}}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %+v, expected %+v", actual, expected)
	}
}

func TestTouchedMapsJavaToTheBuildModuleAndGoToTheGoModule(t *testing.T) {
	// given
	goMod := Module{Kind: Go, Dir: "/repo", BuildRoot: "/repo", Runner: "go"}
	api := Module{Kind: Maven, Dir: "/repo/api", BuildRoot: "/repo/api", Runner: "mvn"}

	// when
	actual := Touched([]Module{goMod, api}, "/repo", []string{"api/gen/x.go", "api/src/A.java"})

	// then
	expected := []Touch{{Module: goMod, Files: []string{"api/gen/x.go"}}, {Module: api, Files: []string{"api/src/A.java"}}}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %+v, expected %+v", actual, expected)
	}
}

func TestTouchedRunsNoToolchainForAChangeWithoutSourceFiles(t *testing.T) {
	// given
	goMod := Module{Kind: Go, Dir: "/repo", BuildRoot: "/repo", Runner: "go"}
	api := Module{Kind: Maven, Dir: "/repo/api", BuildRoot: "/repo/api", Runner: "mvn"}

	// when
	actual := Touched([]Module{goMod, api}, "/repo", []string{"README.md", "api/pom.xml", "docs/x.md"})

	// then
	if actual == nil || len(actual) != 0 {
		t.Fatalf("actual %#v, expected an empty slice", actual)
	}
}

func TestTouchedIgnoresAJavaFileOutsideEveryJavaModule(t *testing.T) {
	// given
	goMod := Module{Kind: Go, Dir: "/repo", BuildRoot: "/repo", Runner: "go"}

	// when
	actual := Touched([]Module{goMod}, "/repo", []string{"scripts/Tool.java"})

	// then
	if actual == nil || len(actual) != 0 {
		t.Fatalf("actual %#v, expected an empty slice", actual)
	}
}
