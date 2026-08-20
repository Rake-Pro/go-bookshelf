// Command checkweb is the frontend's only static check.
//
// There is no Node toolchain in this project and none is wanted, so this
// stands in for a bundler's resolution pass: it walks every JavaScript module
// under web/dist, resolves each import specifier on disk, verifies that every
// named import is actually exported by the module it comes from, and checks
// that every asset referenced by index.html, the manifest and the service
// worker's precache list exists.
//
// Usage: go run ./scripts/checkweb [-dir web/dist]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// dynamicAllowed are import specifiers that legitimately do not resolve: they
// live in vendored code paths this app never evaluates (makeBook, initTTS).
var dynamicAllowed = map[string]bool{
	"./mobi.js":          true,
	"./pdf.js":           true,
	"./fb2.js":           true,
	"./comic-book.js":    true,
	"./tts.js":           true,
	"./vendor/fflate.js": true,
	"./vendor/zip.js":    true,
	"./dict.js":          true,
	"./footnotes.js":     true,
}

var (
	// import ... from 'x'  /  export ... from 'x'  /  import 'x'
	reFrom    = regexp.MustCompile(`(?m)(?:^|[\s;}])(?:import|export)\s+(?:[^'"();]*?\sfrom\s+)?['"]([^'"]+)['"]`)
	reDynamic = regexp.MustCompile(`import\s*\(\s*['"]([^'"]+)['"]\s*\)`)
	// the binding list of `import {a, b as c} from 'x'`
	reNamed = regexp.MustCompile(`(?s)import\s*\{([^}]*)\}\s*from\s*['"]([^'"]+)['"]`)
	// export declarations
	reExportDecl = regexp.MustCompile(`(?m)^\s*export\s+(?:async\s+)?(?:function\*?|class|const|let|var)\s+([A-Za-z_$][\w$]*)`)
	reExportList = regexp.MustCompile(`(?m)^\s*export\s*\{([^}]*)\}`)
	reExportDef  = regexp.MustCompile(`(?m)^\s*export\s+default\b`)
	// href="..." / src="..." in HTML
	reAttr = regexp.MustCompile(`(?:href|src)="([^"]+)"`)
	// the SHELL precache array in sw.js
	reShell = regexp.MustCompile(`(?s)const SHELL = \[(.*?)\]`)
	reQuote = regexp.MustCompile(`'([^']+)'`)
)

func main() {
	dir := flag.String("dir", "web/dist", "the built frontend directory")
	flag.Parse()

	var problems []string
	report := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	modules, err := jsFiles(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if len(modules) == 0 {
		fmt.Fprintf(os.Stderr, "no JavaScript found under %s\n", *dir)
		os.Exit(2)
	}

	exports := map[string]map[string]bool{}
	imports := 0

	for _, rel := range modules {
		src, err := os.ReadFile(filepath.Join(*dir, filepath.FromSlash(rel)))
		if err != nil {
			report("%s: %v", rel, err)
			continue
		}
		exports[rel] = exportedNames(string(src))
	}

	for _, rel := range modules {
		raw, err := os.ReadFile(filepath.Join(*dir, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		src := string(raw)

		specs := map[string]bool{}
		for _, m := range reFrom.FindAllStringSubmatch(src, -1) {
			specs[m[1]] = true
		}
		for _, m := range reDynamic.FindAllStringSubmatch(src, -1) {
			specs[m[1]] = true
		}
		for spec := range specs {
			imports++
			if dynamicAllowed[spec] {
				continue
			}
			if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") && !strings.HasPrefix(spec, "/") {
				report("%s: bare import %q - this app resolves no bare specifiers", rel, spec)
				continue
			}
			target := resolve(rel, spec)
			if _, ok := exports[target]; !ok {
				report("%s: import %q does not resolve (%s is missing)", rel, spec, target)
			}
		}

		for _, m := range reNamed.FindAllStringSubmatch(src, -1) {
			target := resolve(rel, m[2])
			names, ok := exports[target]
			if !ok || dynamicAllowed[m[2]] {
				continue
			}
			for _, binding := range strings.Split(m[1], ",") {
				name := strings.TrimSpace(strings.Split(binding, " as ")[0])
				name = strings.TrimSpace(strings.TrimPrefix(name, "type "))
				if name == "" {
					continue
				}
				if !names[name] {
					report("%s: imports %q from %s, which does not export it", rel, name, target)
				}
			}
		}
	}

	// index.html references.
	assets := 0
	indexPath := filepath.Join(*dir, "index.html")
	if html, err := os.ReadFile(indexPath); err == nil {
		for _, m := range reAttr.FindAllStringSubmatch(string(html), -1) {
			ref := m[1]
			if !strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "//") {
				continue
			}
			assets++
			if !exists(*dir, ref) {
				report("index.html references %s, which is not in %s", ref, *dir)
			}
		}
	} else {
		report("%v", err)
	}

	// Manifest icons.
	if b, err := os.ReadFile(filepath.Join(*dir, "manifest.webmanifest")); err == nil {
		var m struct {
			Icons []struct {
				Src string `json:"src"`
			} `json:"icons"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			report("manifest.webmanifest is not valid JSON: %v", err)
		}
		for _, icon := range m.Icons {
			assets++
			if !exists(*dir, icon.Src) {
				report("manifest.webmanifest lists icon %s, which is missing", icon.Src)
			}
		}
	} else {
		report("%v", err)
	}

	// Service worker precache list and cache version.
	if b, err := os.ReadFile(filepath.Join(*dir, "sw.js")); err == nil {
		sw := string(b)
		if !regexp.MustCompile(`(?m)^const VERSION = '[^']+'`).MatchString(sw) {
			report("sw.js has no VERSION cache-busting constant")
		}
		if m := reShell.FindStringSubmatch(sw); m != nil {
			for _, q := range reQuote.FindAllStringSubmatch(m[1], -1) {
				ref := q[1]
				if ref == "/" {
					continue
				}
				assets++
				if !exists(*dir, ref) {
					report("sw.js precaches %s, which is missing", ref)
				}
			}
		} else {
			report("sw.js has no SHELL precache list")
		}
	} else {
		report("%v", err)
	}

	sort.Strings(problems)
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, p)
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d problem(s)\n", len(problems))
		os.Exit(1)
	}
	fmt.Printf("checkweb: %d modules, %d import specifiers, %d asset references - all resolve\n",
		len(modules), imports, assets)
}

// jsFiles lists every .js file under dir, as slash-separated relative paths.
func jsFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(p) != ".js" {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, err
}

// resolve turns an import specifier inside module `from` into a dist-relative
// path. Absolute specifiers are already dist-relative.
func resolve(from, spec string) string {
	if strings.HasPrefix(spec, "/") {
		return strings.TrimPrefix(path.Clean(spec), "/")
	}
	return path.Clean(path.Join(path.Dir(from), spec))
}

func exists(dir, ref string) bool {
	ref = strings.SplitN(ref, "?", 2)[0]
	ref = strings.SplitN(ref, "#", 2)[0]
	if ref == "/" {
		ref = "/index.html"
	}
	info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(ref, "/"))))
	return err == nil && !info.IsDir()
}

// exportedNames collects the names a module exports. It is a regex pass, not a
// parser: it errs towards accepting, so it catches typos and deletions without
// inventing failures on exotic syntax.
func exportedNames(src string) map[string]bool {
	names := map[string]bool{}
	for _, m := range reExportDecl.FindAllStringSubmatch(src, -1) {
		names[m[1]] = true
	}
	for _, m := range reExportList.FindAllStringSubmatch(src, -1) {
		for _, binding := range strings.Split(m[1], ",") {
			parts := strings.Split(strings.TrimSpace(binding), " as ")
			name := strings.TrimSpace(parts[len(parts)-1])
			if name != "" {
				names[name] = true
			}
		}
	}
	if reExportDef.MatchString(src) {
		names["default"] = true
	}
	return names
}
