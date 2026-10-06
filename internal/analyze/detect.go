package analyze

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

const (
	Go     = "go"
	Maven  = "maven"
	Gradle = "gradle"
)

type Module struct {
	Kind, Dir, BuildRoot, Runner string
}

type Touch struct {
	Module Module
	Files  []string
}

var skipped = map[string]bool{".git": true, ".r-loop": true, "vendor": true, "node_modules": true, "testdata": true}

var kindOrder = map[string]int{Go: 0, Maven: 1, Gradle: 2}

func Detect(root string) ([]Module, error) {
	goDirs, pomDirs, gradleDirs, settingsDirs := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipped[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		dir := filepath.Dir(path)
		switch d.Name() {
		case "go.mod":
			goDirs[dir] = true
		case "pom.xml":
			pomDirs[dir] = true
		case "build.gradle", "build.gradle.kts":
			gradleDirs[dir] = true
		case "settings.gradle", "settings.gradle.kts":
			settingsDirs[dir] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	mods := []Module{}
	for dir := range goDirs {
		mods = append(mods, Module{Kind: Go, Dir: dir, BuildRoot: dir, Runner: "go"})
	}
	for dir := range pomDirs {
		buildRoot := outermost(root, dir, pomDirs)
		mods = append(mods, Module{Kind: Maven, Dir: dir, BuildRoot: buildRoot, Runner: runner(buildRoot, "mvnw", "mvn")})
	}
	for dir := range gradleDirs {
		buildRoot := outermost(root, dir, settingsDirs)
		if buildRoot == "" {
			buildRoot = dir
		}
		mods = append(mods, Module{Kind: Gradle, Dir: dir, BuildRoot: buildRoot, Runner: runner(buildRoot, "gradlew", "gradle")})
	}
	sort.Slice(mods, func(i, j int) bool {
		if mods[i].Dir != mods[j].Dir {
			return mods[i].Dir < mods[j].Dir
		}
		return kindOrder[mods[i].Kind] < kindOrder[mods[j].Kind]
	})
	return mods, nil
}

func outermost(root, dir string, marked map[string]bool) string {
	found := ""
	for d := dir; ; d = filepath.Dir(d) {
		if marked[d] {
			found = d
		}
		if d == root || d == filepath.Dir(d) {
			return found
		}
	}
}

func runner(buildRoot, wrapper, bare string) string {
	path := filepath.Join(buildRoot, wrapper)
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path
	}
	return bare
}

func Touched(mods []Module, dir string, files []string) []Touch {
	byModule := map[int][]string{}
	for _, f := range files {
		var kinds []string
		switch {
		case filepath.Ext(f) == ".go" || filepath.Base(f) == "go.mod" || filepath.Base(f) == "go.sum":
			kinds = []string{Go}
		case filepath.Ext(f) == ".java":
			kinds = []string{Maven, Gradle}
		default:
			continue
		}
		abs := filepath.Join(dir, filepath.FromSlash(f))
		best := -1
		for i, m := range mods {
			if !slices.Contains(kinds, m.Kind) || !within(m.Dir, abs) {
				continue
			}
			if best == -1 || len(m.Dir) > len(mods[best].Dir) {
				best = i
			}
		}
		if best >= 0 {
			byModule[best] = append(byModule[best], f)
		}
	}
	touches := []Touch{}
	for i, m := range mods {
		if changed, ok := byModule[i]; ok {
			touches = append(touches, Touch{Module: m, Files: changed})
		}
	}
	return touches
}

func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
