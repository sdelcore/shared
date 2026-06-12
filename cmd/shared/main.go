package main

import (
	"archive/tar"
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
	"path/filepath"
	"strings"
)

const usage = `usage: shared <command> [arguments]

commands:
  deploy [dir] [--name NAME] [--server URL]   deploy a site directory
  list [--server URL]                         list deployed sites
  open NAME [--server URL]                    print and open a site URL
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

	endpoint := strings.TrimRight(*server, "/") + "/api/deploy?site=" + url.QueryEscape(*name)
	resp, err := http.Post(endpoint, "application/gzip", tarball)
	if err != nil {
		fatal("%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		fatal("deploy failed: %s", serverError(body, resp.Status))
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.URL == "" {
		fatal("unexpected response: %s", strings.TrimSpace(string(body)))
	}
	fmt.Println(out.URL)
}

func buildTarball(root string) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		if d.IsDir() && (base == ".git" || base == "node_modules") {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return nil
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
		_, err = io.Copy(tw, f)
		return err
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
		fmt.Printf("%s\t%s\n", site.Name, site.UpdatedAt)
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
	exec.Command("xdg-open", siteURL).Start()
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
