package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// mountCmd mounts the running daemon's WebDAV URL via macOS osascript.
// The resulting volume appears at /Volumes/<name>.localhost — we use a
// *.localhost hostname so the volume gets a meaningful name without
// sudo or /etc/hosts surgery (macOS resolves *.localhost to 127.0.0.1
// natively, and uses the hostname as the volume label).
//
// Why osascript and not `mount_webdav`: `mount_webdav` hangs on
// loopback servers that don't advertise WWW-Authenticate — it waits
// for a credential prompt that never comes, even with no-auth servers.
// The AppleScript `mount volume "..."` verb negotiates correctly,
// works without prompting for unauthenticated localhost mounts, and
// surfaces the result in Finder + on the Desktop.
//
// Why the ".localhost" suffix stays visible (we can't get a bare
// "fslite" volume name): macOS will only resolve names ending in
// .localhost or a real domain from a non-mDNS context. Dropping the
// suffix would need a Bonjour service advertisement (a lot of plumbing
// for a cosmetic change) or a /etc/hosts entry (requires sudo).
// `/Volumes/fslite.localhost` is the floor we can reach without
// either.
type mountCmd struct {
	Agent  string `name:"agent" help:"Specific agent id to mount (default: the singleton). Also used as the default volume name."`
	URL    string `name:"url" help:"WebDAV URL to mount. Skips state lookup. Pairs with --name to control the volume label."`
	Name   string `name:"name" help:"Volume name. The mount appears at /Volumes/<name>.localhost. Defaults to the agent id (or 'fslite' if no daemon)."`
	NoOpen bool   `name:"no-open" help:"Don't auto-open the mountpoint in Finder."`
}

func (m *mountCmd) Run() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("fslite mount: only supported on macOS (runtime is %s)", runtime.GOOS)
	}

	// Resolve the underlying daemon URL + a default volume name.
	daemonURL := m.URL
	volumeName := m.Name
	var resolvedAgent string

	if daemonURL == "" {
		s, err := loadLiveAgentState(m.Agent)
		if err != nil {
			return err
		}
		daemonURL = s.URL
		resolvedAgent = s.AgentID
	}
	if volumeName == "" {
		if resolvedAgent != "" {
			volumeName = resolvedAgent
		} else if m.Agent != "" {
			volumeName = m.Agent
		} else {
			volumeName = "fslite"
		}
	}

	// Rewrite host → <volumeName>.localhost so Finder displays a
	// meaningful label. Port comes from the original URL.
	u, err := url.Parse(daemonURL)
	if err != nil {
		return fmt.Errorf("parse daemon URL %q: %w", daemonURL, err)
	}
	port := u.Port()
	host := volumeName + ".localhost"
	if port != "" {
		u.Host = host + ":" + port
	} else {
		u.Host = host
	}
	mountURL := u.String()

	out, err := exec.Command("osascript", "-e",
		fmt.Sprintf(`mount volume "%s"`, mountURL)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript mount volume: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	mp := mountpointFromURL(mountURL)
	fmt.Fprintf(os.Stderr, "fslite mount: mounted %s at %s\n", mountURL, mp)

	// Record the mountpoint so `fslite unmount` finds it without guessing.
	if s, err := readState(); err == nil {
		s.Mountpoint = mp
		_ = writeState(*s)
	}

	if !m.NoOpen {
		_ = exec.Command("open", mp).Run()
	}
	return nil
}

// unmountCmd umounts the volume that fslite mount created (or any
// /Volumes/<name>.localhost fallback if no state is recorded).
type unmountCmd struct {
	Path string `name:"path" help:"Mount path to unmount. Defaults to the recorded mountpoint."`
}

func (u *unmountCmd) Run() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("fslite unmount: only supported on macOS (runtime is %s)", runtime.GOOS)
	}

	path := u.Path
	if path == "" {
		if s, err := readState(); err == nil && s.Mountpoint != "" {
			path = s.Mountpoint
		}
	}
	if path == "" {
		// Best-effort fallback: the default mount path under the new
		// naming scheme.
		path = "/Volumes/fslite.localhost"
	}

	out, err := exec.Command("diskutil", "unmount", path).CombinedOutput()
	if err != nil {
		// diskutil failed — try /sbin/umount as a fallback.
		out2, err2 := exec.Command("/sbin/umount", path).CombinedOutput()
		if err2 != nil {
			return fmt.Errorf("unmount %s: diskutil failed (%s); umount failed (%s)",
				path, strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
		}
	}
	fmt.Fprintf(os.Stderr, "fslite unmount: unmounted %s\n", path)

	if s, err := readState(); err == nil && s.Mountpoint == path {
		s.Mountpoint = ""
		_ = writeState(*s)
	}
	return nil
}

// mountWebDAV mounts daemonURL via osascript at /Volumes/<volumeName>.localhost.
// Returns the resulting mountpoint path. macOS-only.
func mountWebDAV(daemonURL, volumeName string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("mount: only supported on macOS (runtime is %s)", runtime.GOOS)
	}
	u, err := url.Parse(daemonURL)
	if err != nil {
		return "", fmt.Errorf("parse daemon URL %q: %w", daemonURL, err)
	}
	port := u.Port()
	host := volumeName + ".localhost"
	if port != "" {
		u.Host = host + ":" + port
	} else {
		u.Host = host
	}
	mountURL := u.String()

	out, err := exec.Command("osascript", "-e",
		fmt.Sprintf(`mount volume "%s"`, mountURL)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("osascript mount volume: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return mountpointFromURL(mountURL), nil
}

// unmountWebDAV unmounts the given path. Idempotent — silent success if
// already unmounted. macOS-only.
func unmountWebDAV(path string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	out, err := exec.Command("diskutil", "unmount", path).CombinedOutput()
	if err == nil {
		return nil
	}
	out2, err2 := exec.Command("/sbin/umount", path).CombinedOutput()
	if err2 == nil {
		return nil
	}
	return fmt.Errorf("unmount %s: diskutil failed (%s); umount failed (%s)",
		path, strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
}

// mountpointFromURL derives the macOS mount path from a WebDAV URL.
// macOS mounts WebDAV under /Volumes/<host>.
func mountpointFromURL(rawURL string) string {
	rest := strings.TrimPrefix(rawURL, "http://")
	rest = strings.TrimPrefix(rest, "https://")
	host := rest
	if i := strings.IndexAny(rest, ":/"); i >= 0 {
		host = rest[:i]
	}
	return "/Volumes/" + host
}
