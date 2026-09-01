package main

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

const (
	installRoot                 = "/opt/sub2api5hlimit"
	githubLatestURL             = "https://api.github.com/repos/MengStar-L/sub2api5hlimit/releases/latest"
	portalServiceUnit           = "sub2api-limit-portal.service"
	defaultRecoveryStopTimeout  = 90 * time.Second
	defaultRecoveryFileTimeout  = 5 * time.Minute
	defaultRecoveryStartTimeout = 90 * time.Second
)

type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("run %s: %w", name, err)
	}
	return string(output), nil
}

type updaterConfig struct {
	InstallRoot string
	BinDir      string
	PortalPath  string
	UpdaterPath string
	DataDir     string
	Database    string
	BackupDir   string
	UpdateDir   string
	RequestPath string
	StatusPath  string
	JournalPath string
	LockPath    string
	EnvPath     string
	ServiceUnit string

	LatestURL   string
	HTTPClient  *http.Client
	Runner      commandRunner
	Now         func() time.Time
	Wait        func(context.Context, time.Duration) error
	GOOS        string
	GOARCH      string
	AllowHTTP   bool
	AllowedHost func(string) bool
	RequireRoot func() error

	MaxReleaseBytes      int64
	MaxMetadata          int64
	MaxArchive           int64
	MaxExpanded          int64
	ReadyAttempts        int
	ReadyDelay           time.Duration
	RecoveryStopTimeout  time.Duration
	RecoveryFileTimeout  time.Duration
	RecoveryStartTimeout time.Duration
}

func productionConfig() updaterConfig {
	binDir := installRoot + "/bin"
	dataDir := installRoot + "/data"
	updateDir := installRoot + "/update"
	client := &http.Client{
		Timeout: 45 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many HTTP redirects")
			}
			if req.URL.Scheme != "https" || !isGitHubDownloadHost(req.URL.Hostname()) {
				return fmt.Errorf("redirect target is not an approved GitHub HTTPS host")
			}
			return nil
		},
	}
	return updaterConfig{
		InstallRoot: installRoot,
		BinDir:      binDir,
		PortalPath:  binDir + "/sub2api-limit-portal",
		UpdaterPath: binDir + "/sub2api-limit-updater",
		DataDir:     dataDir,
		Database:    dataDir + "/app.db",
		BackupDir:   installRoot + "/backups",
		UpdateDir:   updateDir,
		RequestPath: dataDir + "/update.request",
		StatusPath:  updateDir + "/status.json",
		JournalPath: updateDir + "/transaction.json",
		LockPath:    updateDir + "/apply.lock",
		EnvPath:     installRoot + "/config/sub2api-limit-portal.env",
		ServiceUnit: portalServiceUnit,
		LatestURL:   githubLatestURL,
		HTTPClient:  client,
		Runner:      execCommandRunner{},
		Now:         time.Now,
		Wait: func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
		GOOS:                 runtime.GOOS,
		GOARCH:               runtime.GOARCH,
		AllowedHost:          isGitHubDownloadHost,
		RequireRoot:          requireLinuxRoot,
		MaxReleaseBytes:      2 << 20,
		MaxMetadata:          64 << 10,
		MaxArchive:           256 << 20,
		MaxExpanded:          512 << 20,
		ReadyAttempts:        30,
		ReadyDelay:           time.Second,
		RecoveryStopTimeout:  defaultRecoveryStopTimeout,
		RecoveryFileTimeout:  defaultRecoveryFileTimeout,
		RecoveryStartTimeout: defaultRecoveryStartTimeout,
	}
}

func isGitHubDownloadHost(host string) bool {
	switch host {
	case "api.github.com", "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com", "github-releases.githubusercontent.com":
		return true
	default:
		return false
	}
}
