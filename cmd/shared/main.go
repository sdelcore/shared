package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sdelcore/shared/internal/web"
)

const usage = `usage: shared <command> [arguments]

commands:
  deploy [dir] [--name NAME] [--server URL] [--force]
                                              deploy a site directory
  list [--server URL]                         list deployed sites
  open NAME [--server URL]                    print and open a site URL
  rm NAME [--server URL]                      delete a site and its data
  rollback NAME [--server URL]                roll back to the previous version
  versions NAME [--server URL]                list a site's saved versions
  backup [file] [--server URL]                download a tarball of all server data
  init [dir]                                  scaffold a new site directory
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "deploy":
		cmdDeploy(os.Args[2:])
	case "list":
		cmdList(os.Args[2:])
	case "open":
		cmdOpen(os.Args[2:])
	case "rm":
		cmdRm(os.Args[2:])
	case "rollback":
		cmdRollback(os.Args[2:])
	case "versions":
		cmdVersions(os.Args[2:])
	case "backup":
		cmdBackup(os.Args[2:])
	case "init":
		cmdInit(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "shared: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func defaultServer() string {
	if s := os.Getenv("SHARED_SERVER"); s != "" {
		return s
	}
	return "http://localhost:8787"
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "shared: "+format+"\n", args...)
	os.Exit(1)
}

func cmdDeploy(args []string) {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	name := fs.String("name", "", "site name (default: directory base name)")
	server := fs.String("server", defaultServer(), "shared server URL")
	force := fs.Bool("force", false, "overwrite without checking who deployed last")
	fs.Parse(args)

	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
		fs.Parse(fs.Args()[1:])
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		fatal("%v", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		fatal("%v", err)
	}
	if !info.IsDir() {
		fatal("%s is not a directory", dir)
	}
	if *name == "" {
		*name = strings.ToLower(filepath.Base(abs))
	}

	tarball, err := buildTarball(abs)
	if err != nil {
		fatal("packing %s: %v", dir, err)
	}

	deployer := deployerIdentity()
	prevSeq := loadDeployState(abs, *name, *server)

	// With no local deploy state (fresh checkout, new machine) the server
	// cannot check for us, so ask it who deployed last and warn if it wasn't
	// this identity.
	if prevSeq == 0 && !*force {
		if cur := currentDeploy(*server, *name); cur != nil && cur.Deployer != "" && cur.Deployer != deployer {
			if !confirm(fmt.Sprintf("%s was last deployed by %s at %s — overwrite?", *name, cur.Deployer, cur.Time)) {
				fatal("deploy cancelled")
			}
		}
	}

	endpoint := strings.TrimRight(*server, "/") + "/api/deploy?site=" + url.QueryEscape(*name)
	data := tarball.Bytes()
	status, body := postDeploy(endpoint, data, deployer, prevSeq, *force)

	if status == http.StatusConflict {
		var c struct {
			Deployer string `json:"deployer"`
			Time     string `json:"time"`
		}
		json.Unmarshal(body, &c)
		if c.Deployer == "" {
			c.Deployer = "someone else"
		}
		if c.Time == "" {
			c.Time = "an unknown time"
		}
		if !confirm(fmt.Sprintf("%s was deployed by %s at %s since your last deploy — overwrite?", *name, c.Deployer, c.Time)) {
			fatal("deploy cancelled")
		}
		status, body = postDeploy(endpoint, data, deployer, prevSeq, true)
	}

	if status < 200 || status > 299 {
		fatal("deploy failed: %s", serverError(body, http.StatusText(status)))
	}
	var out struct {
		URL     string `json:"url"`
		Version int64  `json:"version"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.URL == "" {
		fatal("unexpected response: %s", strings.TrimSpace(string(body)))
	}
	if out.Version > 0 {
		saveDeployState(abs, *name, *server, out.Version)
	}
	fmt.Println(out.URL)
}

func postDeploy(endpoint string, data []byte, deployer string, prevSeq int64, force bool) (int, []byte) {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		fatal("%v", err)
	}
	req.Header.Set("Content-Type", "application/gzip")
	if deployer != "" {
		req.Header.Set("X-Shared-Deployer", deployer)
	}
	if prevSeq > 0 {
		req.Header.Set("X-Shared-Prev-Version", strconv.FormatInt(prevSeq, 10))
	}
	if force {
		req.Header.Set("X-Shared-Force", "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func deployerIdentity() string {
	name := os.Getenv("USER")
	if name == "" {
		if u, err := user.Current(); err == nil {
			name = u.Username
		}
	}
	if name == "" {
		name = "unknown"
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return name + "@" + host
}

type deployInfo struct {
	Seq      int64  `json:"seq"`
	Time     string `json:"time"`
	Deployer string `json:"deployer"`
	Source   string `json:"source"`
}

func currentDeploy(server, name string) *deployInfo {
	resp, err := http.Get(strings.TrimRight(server, "/") + "/api/versions?site=" + url.QueryEscape(name))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var out struct {
		Current *deployInfo `json:"current"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return nil
	}
	return out.Current
}

func confirm(prompt string) bool {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

type deployState struct {
	Deploys map[string]int64 `json:"deploys"`
}

func stateFile(dir string) string {
	return filepath.Join(dir, ".shared", "state.json")
}

func stateKey(name, server string) string {
	return name + "@" + strings.TrimRight(server, "/")
}

func loadDeployState(dir, name, server string) int64 {
	data, err := os.ReadFile(stateFile(dir))
	if err != nil {
		return 0
	}
	var st deployState
	if json.Unmarshal(data, &st) != nil {
		return 0
	}
	return st.Deploys[stateKey(name, server)]
}

func saveDeployState(dir, name, server string, seq int64) {
	st := deployState{}
	if data, err := os.ReadFile(stateFile(dir)); err == nil {
		json.Unmarshal(data, &st)
	}
	if st.Deploys == nil {
		st.Deploys = map[string]int64{}
	}
	st.Deploys[stateKey(name, server)] = seq
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(stateFile(dir)), 0o755); err != nil {
		return
	}
	os.WriteFile(stateFile(dir), b, 0o644)
}

func buildTarball(root string) (*bytes.Buffer, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	files := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		base := d.Name()
		if strings.HasPrefix(base, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && base == "node_modules" {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			target, err := os.Stat(path)
			if err != nil {
				return fmt.Errorf("broken symlink %s: %w", rel, err)
			}
			if target.IsDir() {
				return fmt.Errorf("symlink to directory not supported: %s", rel)
			}
			if !target.Mode().IsRegular() {
				return fmt.Errorf("symlink to non-regular file: %s", rel)
			}
			info = target
		} else if !d.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported file type: %s", rel)
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(tw, f); err != nil {
			return err
		}
		files++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	if files == 0 {
		return nil, fmt.Errorf("no files to deploy in %s", root)
	}
	return &buf, nil
}

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	server := fs.String("server", defaultServer(), "shared server URL")
	fs.Parse(args)

	resp, err := http.Get(strings.TrimRight(*server, "/") + "/api/sites")
	if err != nil {
		fatal("%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		fatal("list failed: %s", serverError(body, resp.Status))
	}
	var out struct {
		Sites []struct {
			Name      string `json:"name"`
			UpdatedAt string `json:"updatedAt"`
			Bytes     int64  `json:"bytes"`
			Views     int64  `json:"views"`
			Deployer  string `json:"deployer"`
		} `json:"sites"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		fatal("unexpected response: %s", strings.TrimSpace(string(body)))
	}
	if len(out.Sites) == 0 {
		fmt.Println("(none)")
		return
	}
	for _, site := range out.Sites {
		deployer := site.Deployer
		if deployer == "" {
			deployer = "-"
		}
		fmt.Printf("%s\t%s\t%d views\t%s\t%s\n", site.Name, humanSize(site.Bytes), site.Views, deployer, site.UpdatedAt)
	}
}

func cmdOpen(args []string) {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	server := fs.String("server", defaultServer(), "shared server URL")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: shared open NAME [--server URL]")
		os.Exit(2)
	}
	name := fs.Arg(0)
	fs.Parse(fs.Args()[1:])

	u, err := url.Parse(*server)
	if err != nil || u.Host == "" {
		fatal("invalid server URL %q", *server)
	}
	host := u.Host
	if h, p, err := net.SplitHostPort(u.Host); err == nil {
		host = net.JoinHostPort(name+"."+h, p)
	} else {
		host = name + "." + host
	}
	siteURL := u.Scheme + "://" + host + "/"
	fmt.Println(siteURL)
	switch runtime.GOOS {
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", siteURL).Start()
	case "darwin":
		exec.Command("open", siteURL).Start()
	default:
		exec.Command("xdg-open", siteURL).Start()
	}
}

func cmdRm(args []string) {
	fs := flag.NewFlagSet("rm", flag.ExitOnError)
	server := fs.String("server", defaultServer(), "shared server URL")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: shared rm NAME [--server URL]")
		os.Exit(2)
	}
	name := fs.Arg(0)
	fs.Parse(fs.Args()[1:])

	endpoint := strings.TrimRight(*server, "/") + "/api/sites/" + url.PathEscape(name)
	req, err := http.NewRequest(http.MethodDelete, endpoint, nil)
	if err != nil {
		fatal("%v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		fatal("rm failed: %s", serverError(body, resp.Status))
	}
	fmt.Printf("deleted %s\n", name)
}

func cmdRollback(args []string) {
	fs := flag.NewFlagSet("rollback", flag.ExitOnError)
	server := fs.String("server", defaultServer(), "shared server URL")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: shared rollback NAME [--server URL]")
		os.Exit(2)
	}
	name := fs.Arg(0)
	fs.Parse(fs.Args()[1:])

	endpoint := strings.TrimRight(*server, "/") + "/api/rollback?site=" + url.QueryEscape(name)
	req, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		fatal("%v", err)
	}
	req.Header.Set("X-Shared-Deployer", deployerIdentity())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		fatal("rollback failed: %s", serverError(body, resp.Status))
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.URL == "" {
		fatal("unexpected response: %s", strings.TrimSpace(string(body)))
	}
	fmt.Println(out.URL)
}

func cmdVersions(args []string) {
	fs := flag.NewFlagSet("versions", flag.ExitOnError)
	server := fs.String("server", defaultServer(), "shared server URL")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: shared versions NAME [--server URL]")
		os.Exit(2)
	}
	name := fs.Arg(0)
	fs.Parse(fs.Args()[1:])

	resp, err := http.Get(strings.TrimRight(*server, "/") + "/api/versions?site=" + url.QueryEscape(name))
	if err != nil {
		fatal("%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		fatal("versions failed: %s", serverError(body, resp.Status))
	}
	var out struct {
		Current  *deployInfo `json:"current"`
		Versions []struct {
			Timestamp int64 `json:"timestamp"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		fatal("unexpected response: %s", strings.TrimSpace(string(body)))
	}
	if out.Current != nil {
		deployer := out.Current.Deployer
		if deployer == "" {
			deployer = "unknown"
		}
		fmt.Printf("current\tv%d\t%s (%s)\t%s\n", out.Current.Seq, deployer, out.Current.Source, out.Current.Time)
	}
	if len(out.Versions) == 0 {
		fmt.Println("(none)")
		return
	}
	for _, v := range out.Versions {
		fmt.Printf("%d\t%s\n", v.Timestamp, time.Unix(v.Timestamp, 0).Format(time.RFC3339))
	}
}

func cmdBackup(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	server := fs.String("server", defaultServer(), "shared server URL")
	fs.Parse(args)

	dest := ""
	if fs.NArg() > 0 {
		dest = fs.Arg(0)
		fs.Parse(fs.Args()[1:])
	}
	if dest == "" {
		dest = "shared-backup-" + time.Now().Format("20060102-150405") + ".tar.gz"
	}

	resp, err := http.Get(strings.TrimRight(*server, "/") + "/api/export")
	if err != nil {
		fatal("%v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(resp.Body)
		fatal("backup failed: %s", serverError(body, resp.Status))
	}

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		fatal("%v", err)
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		fatal("writing %s: %v", dest, err)
	}
	fmt.Printf("%s\t%s\n", dest, humanSize(n))
}

func cmdInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	fs.Parse(args)

	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fatal("%v", err)
	}

	files := []struct {
		path    string
		content []byte
	}{
		{filepath.Join(dir, "index.html"), web.InitIndexHTML},
		{filepath.Join(dir, ".claude", "skills", "shared-sites", "SKILL.md"), web.InitSkillMD},
	}
	for _, file := range files {
		if _, err := os.Stat(file.path); err == nil {
			fmt.Printf("skip %s (exists)\n", file.path)
			continue
		} else if !os.IsNotExist(err) {
			fatal("%v", err)
		}
		if err := os.MkdirAll(filepath.Dir(file.path), 0o755); err != nil {
			fatal("%v", err)
		}
		if err := os.WriteFile(file.path, file.content, 0o644); err != nil {
			fatal("writing %s: %v", file.path, err)
		}
		fmt.Printf("wrote %s\n", file.path)
	}
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func serverError(body []byte, status string) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	if msg := strings.TrimSpace(string(body)); msg != "" {
		return msg
	}
	return status
}
