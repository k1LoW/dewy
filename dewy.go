package dewy

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/carlescere/scheduler"
	starter "github.com/lestrrat-go/server-starter"
	"github.com/linyows/dewy/kvs"
	"github.com/linyows/dewy/notice"
)

const (
	ISO8601     string = "20060102T150405Z0700"
	releaseDir  string = ISO8601
	releasesDir string = "releases"
	symlinkDir  string = "current"
)

type Dewy struct {
	config          Config
	repository      Repository
	cache           kvs.KVS
	isServerRunning bool
	sync.RWMutex
	root   string
	job    *scheduler.Job
	notice notice.Notice
}

func New(c Config) *Dewy {
	kv := &kvs.File{}
	kv.Default()

	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	return &Dewy{
		config:          c,
		cache:           kv,
		isServerRunning: false,
		root:            wd,
	}
}

func (d *Dewy) Start(i int) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d.notice = notice.New(&notice.Slack{
		Name:    fmt.Sprintf("%s/%s", d.config.Repository.Owner, d.config.Repository.Name),
		Link:    "https://" + d.config.Repository.String(),
		Host:    hostname(),
		Token:   os.Getenv("SLACK_TOKEN"),
		Channel: os.Getenv("SLACK_CHANNEL"),
	})

	cwd, err := os.Getwd()
	user, err := user.Current()
	if err != nil {
		panic(err.Error())
	}
	var fields []*notice.Field
	fields = append(fields, &notice.Field{Title: "Command", Value: d.config.Command.String(), Short: true})
	fields = append(fields, &notice.Field{Title: "User", Value: user.Name, Short: true})
	fields = append(fields, &notice.Field{Title: "Artifact", Value: d.config.Repository.Artifact, Short: true})
	fields = append(fields, &notice.Field{Title: "Working directory", Value: cwd, Short: false})
	d.notice.Notify("Automatic shipping started by Dewy", fields, ctx)

	d.job, err = scheduler.Every(i).Seconds().Run(func() {
		d.Run()
	})
	if err != nil {
		log.Printf("[ERROR] Scheduler failure: %#v", err)
	}

	d.waitSigs()
}

func (d *Dewy) waitSigs() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	sigReceived := <-sigCh
	log.Printf("[DEBUG] PID %d received signal as %s", os.Getpid(), sigReceived)
	d.job.Quit <- true
	d.notice.Notify(fmt.Sprintf("Stop receiving %s signal", sigReceived), nil, ctx)
}

func (d *Dewy) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d.config.Repository.String()
	d.repository = NewRepository(d.config.Repository, d.cache)

	if err := d.repository.Fetch(); err != nil {
		log.Printf("[ERROR] Fetch failure: %#v", err)
		return err
	}

	if !d.repository.IsDownloadNecessary() {
		log.Print("[DEBUG] Download skipped")
		return nil
	}

	key, err := d.repository.Download()
	if err != nil {
		log.Printf("[DEBUG] Download failure: %#v", err)
		return nil
	}

	d.notice.Notify(fmt.Sprintf("New release <%s|%s> was downloaded",
		d.repository.ReleaseHTMLURL(), d.repository.ReleaseTag()), nil, ctx)

	if err := d.deploy(key); err != nil {
		return err
	}

	if d.config.Command != SERVER {
		return nil
	}

	if d.isServerRunning {
		d.notice.Notify("Server restarting", nil, ctx)
		err = d.restartServer()
	} else {
		d.notice.Notify("Server starting", nil, ctx)
		err = d.startServer()
	}

	d.finalizeDeploy()
	return err
}

func (d *Dewy) deploy(key string) error {
	p := filepath.Join(d.cache.GetDir(), key)
	linkFrom, err := d.preserve(p)
	if err != nil {
		return err
	}

	linkTo := filepath.Join(d.root, symlinkDir)
	if _, err := os.Lstat(linkTo); err == nil {
		os.Remove(linkTo)
	}

	log.Printf("[INFO] Create symlink to %s from %s", linkTo, linkFrom)
	if err := os.Symlink(linkFrom, linkTo); err != nil {
		return err
	}

	return nil
}

func (d *Dewy) preserve(p string) (string, error) {
	dst := filepath.Join(d.root, releasesDir, time.Now().UTC().Format(releaseDir))
	if err := os.MkdirAll(dst, 0755); err != nil {
		return "", err
	}

	if err := kvs.ExtractArchive(p, dst); err != nil {
		return "", err
	}
	log.Printf("[INFO] Extract archive to %s", dst)

	return dst, nil
}

func (d *Dewy) restartServer() error {
	d.Lock()
	defer d.Unlock()

	p, _ := os.FindProcess(os.Getpid())
	err := p.Signal(syscall.SIGHUP)
	if err != nil {
		return err
	}
	log.Print("[INFO] Send SIGHUP for server restart")

	return nil
}

func (d *Dewy) startServer() error {
	d.Lock()
	defer d.Unlock()

	d.isServerRunning = true

	log.Print("[INFO] Start server")
	ch := make(chan error)

	go func() {
		s, err := starter.NewStarter(d.config.Starter)
		if err != nil {
			log.Printf("[ERROR] Starter failure: %#v", err)
			return
		}

		ch <- s.Run()
	}()

	return nil
}

func (d *Dewy) finalizeDeploy() {
	log.Print("[DEBUG] Deploy finalizing")

	err := d.repository.Record()
	if err != nil {
		log.Printf("[ERROR] Record failure: %#v", err)
	}
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return fmt.Sprintf("%#v", err)
	}
	return strings.ToLower(name)
}
