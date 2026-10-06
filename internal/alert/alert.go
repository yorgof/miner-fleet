// Package alert notifies on two events: a miner reports a new block found,
// or a miner's best-ever difficulty crosses a configured threshold. It
// publishes to an ntfy (https://ntfy.sh) topic - a plain HTTPS POST, no
// credentials to manage - which delivers a push notification and,
// optionally, an email via ntfy's own server-side relay (the `X-Email`
// header), so this needs no SMTP setup of its own.
//
// It takes plain values (not miners.Stats) deliberately, so this package
// never needs to import package miners - the registry that drives polling
// needs to import THIS package to call it after every poll, and Go doesn't
// allow the reverse import too.
package alert

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/yorgof/miner-fleet/internal/store"
)

// Config is empty-safe: an unconfigured Config (no NtfyURL) makes Enabled()
// false and every Check call a no-op, so alerting is entirely optional -
// nothing here requires ntfy settings to run the rest of the app.
type Config struct {
	NtfyURL   string // full topic URL, e.g. https://ntfy.sh/your-topic-here
	NtfyEmail string // optional: if set, ntfy also emails this address
	// NtfyToken is required for NtfyEmail to actually work on ntfy.sh: the
	// public server rejects email relay from anonymous (unauthenticated)
	// publishes with HTTP 400 "anonymous email sending is not allowed" -
	// confirmed 2026-08-24. Generate one at https://ntfy.sh (Account ->
	// Access tokens) and set it here; push-only alerts work fine without it.
	NtfyToken     string
	DiffThreshold float64
}

func (c Config) Enabled() bool { return c.NtfyURL != "" }

type Notifier struct {
	cfg    Config
	st     *store.Store
	client *http.Client
}

func NewNotifier(cfg Config, st *store.Store) *Notifier {
	return &Notifier{cfg: cfg, st: st, client: &http.Client{Timeout: 10 * time.Second}}
}

func (n *Notifier) Enabled() bool { return n.cfg.Enabled() }

// Health is called only for persistent incident transitions, not every poll.
func (n *Notifier) Health(ctx context.Context, id int64, name, kind, message string, recovered bool) {
	if !n.Enabled() {
		return
	}
	priority := "high"
	tags := "warning"
	if recovered {
		priority = "default"
		tags = "white_check_mark"
	}
	n.notify(ctx, name+": "+kind, message, tags, priority, "health", id)
}

// Check inspects one successful poll's blocksFound/bestDiff for a miner and
// notifies exactly once per new event:
//   - once per increase in blocksFound (a device-reported counter)
//   - once ever per miner crossing DiffThreshold - bestDiff is each
//     device's all-time-best share, which only ever goes up, so "once ever"
//     (not "once per session") is the correct de-dup rule.
//
// Errors (network failures, state load/save failures) are logged, never
// returned - a flaky notification service must not interrupt polling.
func (n *Notifier) Check(ctx context.Context, minerID int64, minerName string, blocksFound int64, bestDiff float64) {
	if !n.Enabled() {
		return
	}
	state, err := n.st.GetAlertState(ctx, minerID)
	if err != nil {
		log.Printf("alert: load state for miner %d: %v", minerID, err)
		return
	}
	changed := false

	if blocksFound > state.LastBlocksFound {
		n.notify(ctx,
			fmt.Sprintf("%s found a block!", minerName),
			fmt.Sprintf(
				"%s just reported a block found (%d total, was %d).\n\n"+
					"Confirm it in the solo-mining pool logs and check the payout address.\n\n"+
					"Best difficulty at time of alert: %s",
				minerName, blocksFound, state.LastBlocksFound, formatDiff(bestDiff)),
			"tada,rotating_light", "urgent", "block-found", minerID)
		state.LastBlocksFound = blocksFound
		changed = true
	}

	if !state.HighDiffAlerted && n.cfg.DiffThreshold > 0 && bestDiff >= n.cfg.DiffThreshold {
		n.notify(ctx,
			fmt.Sprintf("%s hit a high-difficulty share", minerName),
			fmt.Sprintf(
				"%s's best difficulty just crossed your alert threshold.\n\n"+
					"Best difficulty: %s\nThreshold: %s\n\n"+
					"This does NOT mean a block was found - see the separate "+
					"block-found alert for that - but it's an unusually lucky "+
					"share worth a look.",
				minerName, formatDiff(bestDiff), formatDiff(n.cfg.DiffThreshold)),
			"star", "high", "high-difficulty", minerID)
		state.HighDiffAlerted = true
		changed = true
	}

	if changed {
		if err := n.st.SetAlertState(ctx, minerID, state); err != nil {
			log.Printf("alert: save state for miner %d: %v", minerID, err)
		}
	}
}

func (n *Notifier) notify(ctx context.Context, title, message, tags, priority, kind string, minerID int64) {
	if err := publish(ctx, n.client, n.cfg, title, message, tags, priority); err != nil {
		log.Printf("alert: publish %s notification for miner %d: %v", kind, minerID, err)
		return
	}
	log.Printf("alert: sent %s notification for miner %d", kind, minerID)
}

// SendTest publishes one notification immediately so the ntfy topic (and
// email relay, if configured) can be verified without waiting for a real
// event - see the -alert-test flag.
func (n *Notifier) SendTest(ctx context.Context) error {
	if !n.Enabled() {
		return fmt.Errorf("alerting is not configured (set MINER_FLEET_NTFY_URL)")
	}
	return publish(ctx, &http.Client{Timeout: 10 * time.Second}, n.cfg,
		"Miner Fleet: test alert",
		"This is a test notification from miner-fleet. If you got this, alerting is configured correctly.",
		"white_check_mark", "default")
}

func formatDiff(v float64) string {
	switch {
	case v >= 1e12:
		return fmt.Sprintf("%.2fT", v/1e12)
	case v >= 1e9:
		return fmt.Sprintf("%.2fG", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.2fM", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.2fK", v/1e3)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

// publish sends one ntfy message: https://docs.ntfy.sh/publish/. The body
// is the plain-text message; Title/Priority/Tags ride as headers so the
// body stays exactly what's read. X-Email, if set, asks ntfy's own server
// to additionally relay the message by email - no SMTP credentials needed
// on this end.
func publish(ctx context.Context, client *http.Client, cfg Config, title, message, tags, priority string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.NtfyURL, bytes.NewBufferString(message))
	if err != nil {
		return err
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", priority)
	req.Header.Set("Tags", tags)
	if cfg.NtfyEmail != "" {
		req.Header.Set("X-Email", cfg.NtfyEmail)
	}
	if cfg.NtfyToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.NtfyToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy publish failed or timed out")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned HTTP %d", resp.StatusCode)
	}
	return nil
}
