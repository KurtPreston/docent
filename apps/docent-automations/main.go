package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/docentconfig"
	"github.com/KurtPreston/docent/libs/config/userdata"
)

func main() {
	fs := flag.NewFlagSet("docent-automations", flag.ExitOnError)
	stateDir := fs.String("state-dir", "", "state directory (default: XDG state dir)")
	once := fs.Bool("once", false, "process pending jobs once and exit")
	poll := fs.Duration("poll", 5*time.Second, "poll interval when running as a daemon")
	provider := fs.String("provider", "cursor", "default agent provider (cursor|claude)")
	_ = fs.Parse(os.Args[1:])

	dir := *stateDir
	if dir == "" {
		dir = docentconfig.StateDir()
	}
	runner := automation.AgentRunner{
		DefaultProvider: *provider,
		ResolveRemote:   automation.ResolveRemoteURL,
		StateDir:        dir,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *once {
		if err := drainOnce(ctx, dir, runner); err != nil {
			log.Fatal(err)
		}
		return
	}

	log.Printf("docent-automations watching %s (poll %s)", automation.QueueDir(dir), *poll)
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	for {
		if err := drainOnce(ctx, dir, runner); err != nil {
			log.Printf("drain: %v", err)
		}
		select {
		case <-ctx.Done():
			log.Printf("shutting down")
			return
		case <-ticker.C:
		}
	}
}

func drainOnce(ctx context.Context, stateDir string, runner automation.AgentRunner) error {
	jobs, err := automation.ListPendingJobs(stateDir)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		claimed, ok, err := automation.ClaimJob(stateDir, j.ID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		log.Printf("running job %s rule=%s", claimed.ID, claimed.RuleID)
		jobRunner := runner
		jobRunner.Commenter = commenterForJob(claimed)
		if err := automation.ProcessAgentJob(ctx, stateDir, claimed, jobRunner); err != nil {
			log.Printf("job %s failed: %v", claimed.ID, err)
			continue
		}
		log.Printf("job %s done", claimed.ID)
	}
	return nil
}

// commenterForJob builds a JIRA commenter from the directive persisted on the
// job, so post.jira_comment steps run with real credentials. Returns nil when
// the job carries no JIRA directive (the runner then errors only if a comment
// post-step is actually requested).
func commenterForJob(job automation.DurableJob) automation.IssueCommenter {
	if len(job.JiraDirective) == 0 {
		return nil
	}
	var dir userdata.Directive
	if err := json.Unmarshal(job.JiraDirective, &dir); err != nil {
		log.Printf("job %s: bad jira directive: %v", job.ID, err)
		return nil
	}
	reg := collectors.NewRegistry(nil)
	opts := &collectors.CollectOpts{UserdataDir: job.ConfigDir}
	return automation.IssueCommenterFunc(func(ctx context.Context, issueKey, body string) error {
		return reg.PostComment(ctx, dir, opts, issueKey, body)
	})
}
