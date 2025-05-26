package pkg

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	semver "github.com/Masterminds/semver/v3"
)

type PackageMeta struct {
	DistTags map[string]string `json:"dist-tags"`
	Versions map[string]struct {
		Dist struct {
			Tarball string `json:"tarball"`
		} `json:"dist"`
		Dependencies map[string]string `json:"dependencies"`
	} `json:"versions"`
}

type PackageInfo struct {
	Name    string
	Version string
}

var installed = make(map[string]bool)

func Parse(arg string) (PackageInfo, error) {
	if arg == "" {
		return PackageInfo{}, errors.New("package name cannot be empty")
	}

	if strings.Contains(arg, "@") && !strings.HasPrefix(arg, "@") {
		parts := strings.SplitN(arg, "@", 2)
		if parts[0] == "" {
			return PackageInfo{}, errors.New("package name cannot be empty before '@'")
		}
		return PackageInfo{Name: parts[0], Version: parts[1]}, nil
	} else if strings.HasPrefix(arg, "@") {
		at := strings.LastIndex(arg, "@")
		if at > 0 {
			name := arg[:at]
			if name == "" {
				return PackageInfo{}, errors.New("invalid scoped package")
			}
			return PackageInfo{Name: name, Version: arg[at+1:]}, nil
		}
		if strings.Count(arg, "/") < 1 {
			return PackageInfo{}, errors.New("invalid scoped package name")
		}
		return PackageInfo{Name: arg, Version: "latest"}, nil
	}

	if strings.Contains(arg, " ") {
		return PackageInfo{}, errors.New("package name cannot contain spaces")
	}

	return PackageInfo{Name: arg, Version: "latest"}, nil
}

func Install(pkg PackageInfo) error {
	key := pkg.Name + "@" + pkg.Version
	if installed[key] {
		return nil
	}
	installed[key] = true

	fmt.Printf("Resolving %s@%s...\n", pkg.Name, pkg.Version)
	resolved, tarball, deps, err := resolvePackage(pkg)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", key, err)
	}

	resolvedPkg := PackageInfo{Name: pkg.Name, Version: resolved}
	cachePath, err := getCachePath(resolvedPkg)
	if err != nil {
		return fmt.Errorf("get cache path for %s: %w", key, err)
	}

	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		fmt.Printf("Downloading %s...\n", tarball)
		if err := downloadAndExtract(tarball, cachePath); err != nil {
			return fmt.Errorf("download and extract %s: %w", key, err)
		}
	}

	linkPath := filepath.Join("node_modules", pkg.Name)
	_ = os.RemoveAll(linkPath)
	if err := os.MkdirAll(filepath.Dir(linkPath), 0755); err != nil {
		return fmt.Errorf("create parent dir for %s: %w", linkPath, err)
	}
	if err := os.Symlink(cachePath, linkPath); err != nil {
		return fmt.Errorf("symlink %s -> %s: %w", cachePath, linkPath, err)
	}

	fmt.Printf("Linked %s@%s\n", pkg.Name, resolved)

	for dep, depVer := range deps {
		if err := Install(PackageInfo{Name: dep, Version: depVer}); err != nil {
			return fmt.Errorf("install dep %s@%s: %w", dep, depVer, err)
		}
	}

	return nil
}

func resolvePackage(pkg PackageInfo) (resolved string, tarball string, deps map[string]string, err error) {
	encoded := pkg.Name
	if strings.HasPrefix(pkg.Name, "@") {
		encoded = strings.ReplaceAll(pkg.Name, "/", "%2F")
	}

	resp, err := http.Get("https://registry.npmjs.org/" + encoded)
	if err != nil {
		return "", "", nil, fmt.Errorf("fetch metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	var meta PackageMeta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return "", "", nil, fmt.Errorf("decode metadata: %w", err)
	}

	version := pkg.Version
	if tag, ok := meta.DistTags[version]; ok {
		version = tag
	}

	if vinfo, ok := meta.Versions[version]; ok {
		return version, vinfo.Dist.Tarball, vinfo.Dependencies, nil
	}

	constraint, err := semver.NewConstraint(version)
	if err != nil {
		return "", "", nil, fmt.Errorf("invalid version constraint %q: %w", version, err)
	}

	var matchedVersion string
	var matchedSemver *semver.Version

	for v := range meta.Versions {
		ver, err := semver.NewVersion(v)
		if err != nil {
			continue
		}
		if constraint.Check(ver) {
			if matchedSemver == nil || ver.GreaterThan(matchedSemver) {
				matchedSemver = ver
				matchedVersion = v
			}
		}
	}

	if matchedVersion == "" {
		return "", "", nil, fmt.Errorf("no matching version found for %s@%s", pkg.Name, pkg.Version)
	}

	vinfo := meta.Versions[matchedVersion]
	return matchedVersion, vinfo.Dist.Tarball, vinfo.Dependencies, nil
}

func getCachePath(pkg PackageInfo) (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("get user cache dir: %w", err)
	}
	safe := strings.ReplaceAll(pkg.Name, "/", "_")
	return filepath.Join(cacheDir, "npm-go", safe, pkg.Version), nil
}

func downloadAndExtract(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}

		if !strings.HasPrefix(hdr.Name, "package/") {
			continue
		}

		relPath := strings.TrimPrefix(hdr.Name, "package/")
		target := filepath.Join(dest, relPath)

		if hdr.FileInfo().IsDir() {
			if err := os.MkdirAll(target, hdr.FileInfo().Mode()); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("mkdir parent %s: %w", target, err)
			}
			f, err := os.Create(target)
			if err != nil {
				return fmt.Errorf("create file %s: %w", target, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("copy to %s: %w", target, err)
			}
			f.Close()
		}
	}
	return nil
}
